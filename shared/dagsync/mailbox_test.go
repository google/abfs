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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewMailbox(t *testing.T) {
	mb := NewMailbox()
	mb.Close()
	<-mb.Receive()
}

func TestSendAndReceive(t *testing.T) {
	mb := NewMailbox()
	defer mb.Close()

	msg := "hello"
	mb.Send(msg)

	select {
	case received := <-mb.Receive():
		assert.Equal(t, msg, received)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Timed out waiting for message")
	}
}

func TestFIFOOrder(t *testing.T) {
	mb := NewMailbox()

	messages := []Message{"msg1", "msg2", "msg3", 4, 5.0}
	var received []Message
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for msg := range mb.Receive() {
			received = append(received, msg)
		}
	}()

	for _, msg := range messages {
		mb.Send(msg)
	}

	mb.Close()

	wg.Wait()

	require.Equal(t, messages, received)
}

func TestCloseEmptyMailbox(t *testing.T) {
	mb := NewMailbox()
	mb.Close()

	select {
	case msg, ok := <-mb.Receive():
		require.False(t, ok, "Expected channel to be closed, but received message: %v", msg)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Timed out waiting for channel to close")
	}
}

func TestCloseWithPendingMessages(t *testing.T) {
	mb := NewMailbox()

	mb.Send("msg1")
	mb.Send("msg2")

	mb.Close()

	var received []Message
	for msg := range mb.Receive() {
		received = append(received, msg)
	}

	assert.Equal(t, []Message{"msg1", "msg2"}, received)
}

func TestPanicSendOnClosedMailbox(t *testing.T) {
	mb := NewMailbox()
	mb.Close()

	<-mb.Receive()

	sendBlocked := true
	sendChan := make(chan struct{})
	go func() {
		mb.Send("this should be dropped")
		sendBlocked = false
		close(sendChan)
	}()

	select {
	case <-sendChan:
		if sendBlocked {
			t.Error("Send() did not set flag, but channel closed (race condition?)")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Send() blocked on a closed mailbox")
	}
}

func TestConcurrentProducers(t *testing.T) {
	mb := NewMailbox()

	producerCount := 50
	messagesPerProducer := 100
	expectedTotal := int64(producerCount * messagesPerProducer)

	var receivedCount int64
	var consumerWg sync.WaitGroup
	var producerWg sync.WaitGroup

	// Start the single consumer
	consumerWg.Add(1)
	go func() {
		defer consumerWg.Done()
		for range mb.Receive() {
			receivedCount++
		}
	}()

	for i := 0; i < producerCount; i++ {
		producerWg.Add(1)
		go func(id int) {
			defer producerWg.Done()
			for j := 0; j < messagesPerProducer; j++ {
				mb.Send(j)
			}
		}(i)
	}

	producerWg.Wait()

	mb.Close()

	consumerWg.Wait()

	assert.Equal(t, receivedCount, expectedTotal)
}
