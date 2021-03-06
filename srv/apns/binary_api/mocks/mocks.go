// Package mocks implements a mock APNS server, for unit tests.
// Instead of a TCP socket, the mock connection uses a golang chan of bytes.
package mocks

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

type APNSNotificaton struct {
	command    uint8
	id         uint32
	expiry     uint32
	tokenLen   uint16
	devToken   []byte
	payloadLen uint16
	payload    []byte
}

func (notification *APNSNotificaton) String() string {
	token := hex.EncodeToString(notification.devToken)
	token = strings.ToLower(token)
	return fmt.Sprintf("command=%v; id=%v; expiry=%v; token=%v; payload=%v",
		notification.command, notification.id, notification.expiry, token, string(notification.payload))
}

type APNSResponse struct {
	id     uint32
	status uint8
}

type MockDirectionalConn struct {
	channel chan byte
}

var _ io.ReadWriter = &MockDirectionalConn{}

func newMockDirectionalConn() *MockDirectionalConn {
	return &MockDirectionalConn{
		channel: make(chan byte),
	}
}

func (directionalConn *MockDirectionalConn) Write(b []byte) (n int, err error) {
	for _, x := range b {
		directionalConn.channel <- x
	}
	return len(b), nil
}

func (directionalConn *MockDirectionalConn) Read(b []byte) (int, error) {
	n := len(b)
	for x := 0; x < n; x++ {
		result, ok := <-directionalConn.channel
		if !ok {
			if x != 0 {
				panic("Mock read unexpectedly cut off, shouldn't happen\n")
			}
			return x, errors.New("Mock connection was closed")
		}
		b[x] = result
	}
	return n, nil
}

func (directionalConn *MockDirectionalConn) CleanUp() {
	close(directionalConn.channel)
}

type MockNetConn struct {
	readConn       *MockDirectionalConn
	writeConn      *MockDirectionalConn
	isClientClosed bool
	rwmutex        sync.RWMutex
}

var _ net.Conn = &MockNetConn{}

func NewMockNetConn() *MockNetConn {
	return &MockNetConn{
		readConn:       newMockDirectionalConn(),
		writeConn:      newMockDirectionalConn(),
		isClientClosed: false,
	}
}

func (netConn *MockNetConn) CleanUp() {
	netConn.readConn.CleanUp()
	netConn.rwmutex.Lock()
	defer netConn.rwmutex.Unlock()
	// TODO: attempt read?
	if !netConn.isClientClosed {
		panic("Client code didn't clean up the channel")
	}
}

func (netConn *MockNetConn) Read(b []byte) (n int, err error) {
	netConn.rwmutex.RLock()
	defer netConn.rwmutex.RUnlock()
	return netConn.readConn.Read(b)
}

func (netConn *MockNetConn) Write(b []byte) (n int, err error) {
	netConn.rwmutex.RLock()
	defer netConn.rwmutex.RUnlock()
	return netConn.writeConn.Write(b)
}

func (netConn *MockNetConn) Close() error {
	// Allow isClientClosed to be called multiple times - from resultCollector and from worker pool
	// (e.g. if both of them notice connection issues)
	if !netConn.isClientClosed {
		netConn.writeConn.CleanUp()
		netConn.isClientClosed = true
	}
	return nil
}

func (netConn *MockNetConn) LocalAddr() net.Addr {
	return nil
}

func (netConn *MockNetConn) RemoteAddr() net.Addr {
	return nil
}

func (netConn *MockNetConn) SetDeadline(t time.Time) error {
	netConn.SetReadDeadline(t)
	return netConn.SetWriteDeadline(t)
}

func (netConn *MockNetConn) SetReadDeadline(t time.Time) error {
	return nil
}

func (netConn *MockNetConn) SetWriteDeadline(t time.Time) error {
	return nil
}

func bytesToUint32(data []byte) uint32 {
	if len(data) != 4 {
		panic(fmt.Sprintf("Invalid data provided, expected 4 bytes, got %d", len(data)))
	}
	var result uint32
	err := binary.Read(bytes.NewBuffer(data), binary.BigEndian, &result)
	if err != nil {
		panic(err)
	}
	return result
}

func bytesToUint8(data []byte) uint8 {
	if len(data) != 1 {
		panic(fmt.Sprintf("Invalid data provided, expected 1 byte, got %d", len(data)))
	}
	return data[0]
}

func (netConn *MockNetConn) ReadNotification() (notif *APNSNotificaton, err error) {
	notif = new(APNSNotificaton)
	// Read the bytes that the *tested* code sent
	var conn io.Reader = netConn.writeConn
	err = binary.Read(conn, binary.BigEndian, &(notif.command))
	if err != nil {
		notif = nil
		return
	}

	notifType := notif.command

	var frameLen uint32

	if notifType != 2 {
		panic(fmt.Errorf("Unknown Command %d in request command frame", int(notifType)))
	}

	err = binary.Read(conn, binary.BigEndian, &frameLen)
	if err != nil {
		notif = nil
		return
	}
	// Number of bytes of frame data read so far(after header of command+frame length)
	totalReadLength := int(0)
	totalExpectedLen := int(frameLen)

	// Read a number of bytes.
	// If the expectedByteCount is not 0, assert that the item sets the length field to that value.
	// Note: using panic() because some callers don't immediately fail the test, and the test would hang.
	readItemBytes := func(expectedId uint8, expectedItemLength uint16) []byte {
		decreaseRemainingLen := func(amt int) {
			totalReadLength += amt
			if totalReadLength > totalExpectedLen {
				panic(fmt.Errorf("Read too many bytes reading item %d - Reading %d bytes put means a total of %d were requested, which is larger than the frame length of %d", expectedId, amt, totalReadLength, totalExpectedLen))
			}
		}

		var itemID uint8
		err := binary.Read(conn, binary.BigEndian, &itemID)
		if err != nil {
			panic(err)
		}
		if itemID != expectedId {
			panic(fmt.Errorf("Expected item id %d, but uniqush sent item id %d", expectedId, itemID))
		}
		decreaseRemainingLen(1)
		var itemLength uint16
		err = binary.Read(conn, binary.BigEndian, &itemLength)
		if err != nil {
			panic(err)
		}
		if itemLength > 2048 {
			panic(fmt.Errorf("The largest item len should be 2048, but got len of %d for item %d", itemLength, expectedId))
		}
		if expectedItemLength > 0 && itemLength != expectedItemLength {
			panic(fmt.Errorf("Expected item %d to have length %d, but the client passed a length of %d", expectedId, expectedItemLength, itemLength))
		}
		decreaseRemainingLen(2)

		itemBytes := make([]byte, itemLength)
		_, err = io.ReadFull(conn, itemBytes)
		if err != nil {
			panic(fmt.Errorf("Failed to read %d bytes of item %d", itemLength, expectedId))
		}
		decreaseRemainingLen(int(itemLength))

		return itemBytes
	}

	// Expect the frame generated by uniqush client to APNS to contain 5 item, with ids 1-5, in order of id.

	notif.devToken = readItemBytes(1, 0)         // Some of the tests test lengths other than 32, in case APNS allows longer tokens.
	notif.tokenLen = uint16(len(notif.devToken)) // TODO: remove?

	notif.payload = readItemBytes(2, 0)
	notif.payloadLen = uint16(len(notif.payload))
	if notif.payloadLen < 2 {
		// must be 5 or 10, and we don't send 5 yet
		panic(fmt.Sprintf("Payload length %d is way too short", notif.payloadLen))
	}

	notif.id = bytesToUint32(readItemBytes(3, 4))

	notif.expiry = bytesToUint32(readItemBytes(4, 4))

	priority := bytesToUint8(readItemBytes(5, 1))

	if priority != 10 {
		// must be 5 or 10, and we don't send 5 yet
		panic(fmt.Sprintf("Expected priority 10 (not sending 5 yet), got %d", priority))
	}
	return
}

func (netConn *MockNetConn) Reply(status *APNSResponse) error {
	command := uint8(8)
	// Write to the mock connection half that it's reading *from*
	var conn io.Writer = netConn.readConn
	err := binary.Write(conn, binary.BigEndian, command)
	if err != nil {
		return err
	}
	err = binary.Write(conn, binary.BigEndian, status.status)
	if err != nil {
		return err
	}
	return binary.Write(conn, binary.BigEndian, status.id)
}

func SimulateStableAPNSServer(conn *MockNetConn, statusCode uint8) (int, error) {
	count := 0
	for {
		notif, err := conn.ReadNotification()
		if err != nil {
			return count, err
		}
		status := &APNSResponse{
			id:     notif.id,
			status: statusCode,
		}
		conn.Reply(status)
		count++
	}
}
