// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package dagsync

import (
	"container/list"
	"sync"
)

type Message interface{}

// Mailbox provides a similar interface to a channel, but with an unbound capacity.
type Mailbox struct {
	in   chan Message
	out  chan Message
	done chan struct{}
	once sync.Once

	buffer *list.List
}

// NewMailbox creates, starts, and returns a new Mailbox.
func NewMailbox() *Mailbox {
	mb := &Mailbox{
		in:     make(chan Message),
		out:    make(chan Message),
		done:   make(chan struct{}),
		buffer: list.New(),
	}

	go mb.run()

	return mb
}

// run is the central loop that multiplexes input, output, and shutdown.
func (mb *Mailbox) run() {
	defer close(mb.out)

	for {
		var outChan chan Message
		var nextMsg Message

		if mb.buffer.Len() > 0 {
			outChan = mb.out
			nextMsg = mb.buffer.Front().Value
		}

		select {
		case msg := <-mb.in:
			mb.buffer.PushBack(msg)

		case outChan <- nextMsg:
			element := mb.buffer.Front()
			mb.buffer.Remove(element)

		case <-mb.done:
			for mb.buffer.Len() > 0 {
				element := mb.buffer.Front()
				mb.out <- element.Value
				mb.buffer.Remove(element)
			}
			return
		}
	}
}

// Send posts a message to the mailbox.
func (mb *Mailbox) Send(msg Message) {
	select {
	case mb.in <- msg:
	case <-mb.done:
	}
}

// Receive returns the read-only channel for consuming messages.
func (mb *Mailbox) Receive() <-chan Message {
	return mb.out
}

// Close gracefully shuts down the mailbox.
// Similar to golang channels, it signals there will be no more sends and Receive will be closed after all messages are consumed.
func (mb *Mailbox) Close() {
	mb.once.Do(func() {
		close(mb.done)
	})
}
