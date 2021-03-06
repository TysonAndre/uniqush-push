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

package push

import "fmt"

type Result struct {
	Provider    *PushServiceProvider
	Destination *DeliveryPoint
	Content     *Notification
	MsgID       string
	Err         Error
}

func (r *Result) IsError() bool {
	return r.Err != nil
}

func (r *Result) Error() string {
	if !r.IsError() {
		return fmt.Sprintf("PushServiceProvider=%v DeliveryPoint=%v MsgID=%v Success!",
			r.Provider.Name(),
			r.Destination.Name(),
			r.MsgID)
	}

	return fmt.Sprintf("Failed PushServiceProvider=%s DeliveryPoint=%s %v",
		r.Provider.Name(),
		r.Destination.Name(),
		r.Err)
}

type PushServiceType interface {

	// Passing a pointer to PushServiceProvider allows us
	// to use a memory pool to store a set of empty *PushServiceProvider
	BuildPushServiceProviderFromMap(map[string]string, *PushServiceProvider) error

	BuildDeliveryPointFromMap(map[string]string, *DeliveryPoint) error
	Name() string

	// NOTE: This method should always be run in a separate goroutine.
	// The implementation of this method should return only
	// if it finished all push request.
	//
	// Once this method returns, it cannot use the second channel
	// to report error. (For example, it cannot fork a new goroutine
	// and use this channel in this goroutine after the function returns.)
	//
	// Any implementation MUST close the second channel (chan<- *Result)
	// once the works done.
	Push(*PushServiceProvider, <-chan *DeliveryPoint, chan<- *Result, *Notification)

	// Preview the bytes of a notification, for placeholder subscriber data. This makes no service/database calls.
	Preview(*Notification) ([]byte, Error)

	// Set a channel for the push service provider so that it can report error even if
	// there is no method call on it.
	// The type of the errors sent may cause the push service manager to take various actions.
	SetErrorReportChan(errChan chan<- Error)

	// Set the config for the push service provider.
	// The config for a given pushservicetype is passed to the corresponding PushServiceType
	SetPushServiceConfig(conf *PushServiceConfig)

	Finalize()
}
