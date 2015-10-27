/*
 * Copyright 2011 Nan Deng
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 */

package main

import (
	"sync"
	"time"
	. "github.com/uniqush/log"
	. "github.com/uniqush/uniqush-push/db"
	. "github.com/uniqush/uniqush-push/push"
)

type PushBackEnd struct {
	psm     *PushServiceManager
	db      PushDatabase
	loggers []Logger
	errChan chan error
}

func (self *PushBackEnd) Finalize() {
	self.db.FlushCache()
	close(self.errChan)
	self.psm.Finalize()
}

func NewPushBackEnd(psm *PushServiceManager, database PushDatabase, loggers []Logger) *PushBackEnd {
	ret := new(PushBackEnd)
	ret.psm = psm
	ret.db = database
	ret.loggers = loggers
	ret.errChan = make(chan error)
	go ret.processError()
	psm.SetErrorReportChan(ret.errChan)
	return ret
}

func (self *PushBackEnd) AddPushServiceProvider(service string, psp *PushServiceProvider) error {
	err := self.db.AddPushServiceProviderToService(service, psp)
	if err != nil {
		return err
	}
	return nil
}

func (self *PushBackEnd) RemovePushServiceProvider(service string, psp *PushServiceProvider) error {
	err := self.db.RemovePushServiceProviderFromService(service, psp)
	if err != nil {
		return err
	}
	return nil
}


func (self *PushBackEnd) Subscribe(service, sub string, dp *DeliveryPoint) (*PushServiceProvider, error) {
	psp, err := self.db.AddDeliveryPointToService(service, sub, dp)
	if err != nil {
		return nil, err
	}
	return psp, nil
}

func (self *PushBackEnd) Unsubscribe(service, sub string, dp *DeliveryPoint) error {
	err := self.db.RemoveDeliveryPointFromService(service, sub, dp)
	if err != nil {
		return err
	}
	return nil
}

func (self *PushBackEnd) processError() {
	for err := range self.errChan {
		rid := randomUniqId()
		nullHandler := &NullApiResponseHandler{}
		e := self.fixError(rid, err, self.loggers[LOGGER_PUSH], 0*time.Second, nullHandler)
		switch e0 := e.(type) {
		case *InfoReport:
			self.loggers[LOGGER_PUSH].Infof("%v", e0)
		default:
			self.loggers[LOGGER_PUSH].Errorf("Error: %v", e0)
		}
	}
}

func (self *PushBackEnd) fixError(reqId string, event error, logger Logger, after time.Duration, handler ApiResponseHandler) error {
	var service string
	var sub string
	var ok bool
	if event == nil {
		return nil
	}
	switch err := event.(type) {
	case *RetryError:
		if err.Provider == nil || err.Destination == nil || err.Content == nil {
			return nil
		}
		if service, ok = err.Provider.FixedData["service"]; !ok {
			return nil
		}
		if sub, ok = err.Destination.FixedData["subscriber"]; !ok {
			return nil
		}
		if after <= 1*time.Second {
			after = 5 * time.Second
		}
		providerName := err.Provider.Name()
		destinationName := err.Destination.Name()
		if after > 1*time.Minute {
			logger.Errorf("RequestID=%v Service=%v Subscriber=%v PushServiceProvider=%v DeliveryPoint=%v Failed after retry", reqId, service, sub, providerName, destinationName)
			handler.AddDetailsToHandler(ApiResponseDetails{RequestId: &reqId, Service: &service, Subscriber: &sub, PushServiceProvider: &providerName, DeliveryPoint: &destinationName, Code: UNIQUSH_ERROR_FAILED_RETRY})
			return nil
		}
		logger.Infof("RequestID=%v Service=%v Subscriber=%v PushServiceProvider=%v DeliveryPoint=%v Retry after %v", reqId, service, sub, providerName, destinationName, after)
		go func() {
			<-time.After(after)
			subs := make([]string, 1)
			subs[0] = sub
			after = 2 * after
			self.pushImpl(reqId, service, subs, err.Content, nil, self.loggers[LOGGER_PUSH], err.Provider, err.Destination, after, handler)
		}()
	case *PushServiceProviderUpdate:
		if err.Provider == nil {
			return nil
		}
		if service, ok = err.Provider.FixedData["service"]; !ok {
			return nil
		}
		psp := err.Provider
		e := self.db.ModifyPushServiceProvider(psp)
		pspName := psp.Name()
		if e != nil {
			logger.Errorf("RequestID=%v Service=%v PushServiceProvider=%v Update Failed: %v", reqId, service, pspName, e)
			handler.AddDetailsToHandler(ApiResponseDetails{RequestId: &reqId, Service: &service, PushServiceProvider: &pspName, Code: UNIQUSH_ERROR_UPDATE_PUSH_SERVICE_PROVIDER})
		} else {
			logger.Infof("RequestID=%v Service=%v PushServiceProvider=%v Update Success", reqId, service, pspName)
			handler.AddDetailsToHandler(ApiResponseDetails{RequestId: &reqId, Service: &service, PushServiceProvider: &pspName, Code: UNIQUSH_SUCCESS})
		}
	case *DeliveryPointUpdate:
		if err.Destination == nil {
			return nil
		}
		if sub, ok = err.Destination.FixedData["subscriber"]; !ok {
			return nil
		}
		dp := err.Destination
		e := self.db.ModifyDeliveryPoint(dp)
		dpName := dp.Name()
		if e != nil {
			logger.Errorf("Subscriber=%v DeliveryPoint=%v Update Failed: %v", sub, dpName, e)
			handler.AddDetailsToHandler(ApiResponseDetails{Subscriber: &sub, DeliveryPoint: &dpName, Code: UNIQUSH_ERROR_UPDATE_DELIVERY_POINT})
		} else {
			logger.Infof("Service=%v Subscriber=%v DeliveryPoint=%v Update Success", service, sub, dpName)
			handler.AddDetailsToHandler(ApiResponseDetails{Subscriber: &sub, Service: &service, DeliveryPoint: &dpName, Code: UNIQUSH_SUCCESS})
		}
	case *InvalidRegistrationUpdate:
		if err.Provider == nil || err.Destination == nil {
			return nil
		}
		if service, ok = err.Provider.FixedData["service"]; !ok {
			return nil
		}
		if sub, ok = err.Destination.FixedData["subscriber"]; !ok {
			return nil
		}
		dp := err.Destination
		e := self.Unsubscribe(service, sub, dp)
		dpName := dp.Name()
		if e != nil {
			logger.Errorf("Service=%v Subscriber=%v DeliveryPoint=%v Removing invalid reg failed: %v", service, sub, dpName, e)
			handler.AddDetailsToHandler(ApiResponseDetails{Service: &service, Subscriber: &sub, DeliveryPoint: &dpName, Code: UNIQUSH_REMOVE_INVALID_REG})
		} else {
			logger.Infof("Service=%v Subscriber=%v DeliveryPoint=%v Invalid registration removed", service, sub, dpName)
			handler.AddDetailsToHandler(ApiResponseDetails{Service: &service, Subscriber: &sub, DeliveryPoint: &dpName, Code: UNIQUSH_REMOVE_INVALID_REG})
		}
	case *UnsubscribeUpdate:
		if err.Provider == nil || err.Destination == nil {
			return nil
		}
		if service, ok = err.Provider.FixedData["service"]; !ok {
			return nil
		}
		if sub, ok = err.Destination.FixedData["subscriber"]; !ok {
			return nil
		}
		dp := err.Destination
		e := self.Unsubscribe(service, sub, dp)
		dpName := dp.Name()
		if e != nil {
			logger.Errorf("Service=%v Subscriber=%v DeliveryPoint=%v Unsubscribe failed: %v", service, sub, dpName, e)
			handler.AddDetailsToHandler(ApiResponseDetails{Service: &service, Subscriber: &sub, DeliveryPoint: &dpName, Code: UNIQUSH_UPDATE_UNSUBSCRIBE})
		} else {
			logger.Infof("Service=%v Subscriber=%v DeliveryPoint=%v Unsubscribe success", service, sub, dpName)
			handler.AddDetailsToHandler(ApiResponseDetails{Service: &service, Subscriber: &sub, DeliveryPoint: &dpName, Code: UNIQUSH_UPDATE_UNSUBSCRIBE})
		}
	default:
		return err
	}
	return nil
}

func (self *PushBackEnd) collectResult(reqId string, service string, resChan <-chan *PushResult, logger Logger, after time.Duration, handler ApiResponseHandler) {
	for res := range resChan {
		var sub string
		var ok bool
		if res.Provider != nil && res.Destination != nil {
			if sub, ok = res.Destination.FixedData["subscriber"]; !ok {
				destinationName := res.Destination.Name()
				logger.Errorf("RequestID=%v Subscriber=%v DeliveryPoint=%v Bad Delivery Point: No subscriber", reqId, sub, destinationName)
				handler.AddDetailsToHandler(ApiResponseDetails{RequestId: &reqId, Subscriber: &sub, DeliveryPoint: &destinationName, Code: UNIQUSH_ERROR_BAD_DELIVERY_POINT})
				continue
			}
		}
		if res.Err == nil {
			providerName := res.Provider.Name()
			destinationName := res.Destination.Name()
			msgId := res.MsgId
			logger.Infof("RequestID=%v Service=%v Subscriber=%v PushServiceProvider=%v DeliveryPoint=%v MsgId=%v Success!", reqId, service, sub, providerName, destinationName, msgId)
			handler.AddDetailsToHandler(ApiResponseDetails{RequestId: &reqId, Service: &service, Subscriber: &sub, PushServiceProvider: &providerName, DeliveryPoint: &destinationName, MessageId: &msgId, Code: UNIQUSH_SUCCESS})
			continue
		}
		err := self.fixError(reqId, res.Err, logger, after, handler)
		if err != nil {
			pspName := "Unknown"
			dpName := "Unknown"
			if res.Provider != nil {
				pspName = res.Provider.Name()
			}
			if res.Destination != nil {
				dpName = res.Destination.Name()
			}
			logger.Errorf("RequestID=%v Service=%v Subscriber=%v PushServiceProvider=%v DeliveryPoint=%v Failed: %v", reqId, service, sub, pspName, dpName, err)
			handler.AddDetailsToHandler(ApiResponseDetails{RequestId: &reqId, Service: &service, Subscriber: &sub, PushServiceProvider: &pspName, DeliveryPoint: &dpName, Code: UNIQUSH_ERROR_GENERIC})
		}
	}
}

func (self *PushBackEnd) NumberOfDeliveryPoints(service, sub string, logger Logger) int {
	pspDpList, err := self.db.GetPushServiceProviderDeliveryPointPairs(service, sub)
	if err != nil {
		logger.Errorf("Query=NumberOfDeliveryPoints Service=%v Subscriber=%v Failed: Database Error %v", service, sub, err)
		return 0
	}
	return len(pspDpList)
}

func (self *PushBackEnd) Push(reqId string, service string, subs []string, notif *Notification, perdp map[string][]string, logger Logger, handler ApiResponseHandler) {
	self.pushImpl(reqId, service, subs, notif, perdp, logger, nil, nil, 0*time.Second, handler)
}

func (self *PushBackEnd) pushImpl(reqId string, service string, subs []string, notif *Notification, perdp map[string][]string, logger Logger, provider *PushServiceProvider, dest *DeliveryPoint, after time.Duration, handler ApiResponseHandler) {
	dpChanMap := make(map[string]chan *DeliveryPoint)
	wg := new(sync.WaitGroup)
	for _, sub := range subs {
		dpidx := 0
		var pspDpList []PushServiceProviderDeliveryPointPair
		if provider != nil && dest != nil {
			pspDpList := make([]PushServiceProviderDeliveryPointPair, 1)
			pspDpList[0].PushServiceProvider = provider
			pspDpList[0].DeliveryPoint = dest
		} else {
			var err error
			pspDpList, err = self.db.GetPushServiceProviderDeliveryPointPairs(service, sub)
			if err != nil {
				logger.Errorf("RequestID=%v Service=%v Subscriber=%v Failed: Database Error %v", reqId, service, sub, err)
				handler.AddDetailsToHandler(ApiResponseDetails{RequestId: &reqId, Service: &service, Subscriber: &sub, Code: UNIQUSH_ERROR_DATABASE})
				continue
			}
		}

		if len(pspDpList) == 0 {
			logger.Errorf("RequestID=%v Service=%v Subscriber=%v Failed: No device", reqId, service, sub)
			handler.AddDetailsToHandler(ApiResponseDetails{RequestId: &reqId, Service: &service, Subscriber: &sub, Code: UNIQUSH_ERROR_NO_DEVICE})
			continue
		}

		for _, pair := range pspDpList {
			psp := pair.PushServiceProvider
			dp := pair.DeliveryPoint
			if psp == nil {
				logger.Errorf("RequestID=%v Service=%v Subscriber=%v Failed once: nil Push Service Provider", reqId, service, sub)
				handler.AddDetailsToHandler(ApiResponseDetails{RequestId: &reqId, Service: &service, Subscriber: &sub, Code: UNIQUSH_ERROR_NO_PUSH_SERVICE_PROVIDER})
				continue
			}
			if dp == nil {
				logger.Errorf("RequestID=%v Service=%v Subscriber=%v Failed once: nil Delivery Point", reqId, service, sub)
				handler.AddDetailsToHandler(ApiResponseDetails{RequestId: &reqId, Service: &service, Subscriber: &sub, Code: UNIQUSH_ERROR_NO_DELIVERY_POINT})
				continue
			}
			var ch chan *DeliveryPoint
			var ok bool
			if ch, ok = dpChanMap[psp.Name()]; !ok {
				ch = make(chan *DeliveryPoint)
				dpChanMap[psp.Name()] = ch
				resChan := make(chan *PushResult)
				wg.Add(1)
				note := notif
				if len(perdp) > 0 {
					note = notif.Clone()
					for k, v := range perdp {
						value := v[dpidx%len(v)]
						note.Data[k] = value
					}
					dpidx++
				}
				go func() {
					self.psm.Push(psp, ch, resChan, note)
					wg.Done()
				}()
				wg.Add(1)
				go func() {
					self.collectResult(reqId, service, resChan, logger, after, handler)
					wg.Done()
				}()
			}
			ch <- dp
		}
	}
	for _, dpch := range dpChanMap {
		close(dpch)
	}
	wg.Wait()
}
