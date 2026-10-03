// Copyright 2025 Google LLC
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

package cacheman

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/abfs/cas/hash"
)

const (
	minResponseLength       = 6
	fetchStatResponseLength = 19
)

var ErrClosedClient = errors.New("read/write on closed client")

type Client struct {
	socketPath string

	// In order to prevent deadlocks, these locks must be acquired in the following order:
	// writeLock -> readLock -> connMu
	//
	// writeLock protects the logical write state. It serializes sendMessage and reconnection
	// attempts. Holding writeLock guarantees the connection reference will not be replaced.
	writeLock sync.Mutex
	// readLock protects the logical read state. It serializes the active reader loop (serve)
	// to ensure only one reader processes messages from the connection at any time.
	readLock sync.Mutex
	// connMu protects the memory-level pointer access to the conn field (preventing data races
	// on the interface value itself). It must only be held briefly to copy or clear the conn
	// reference, and never held during I/O or network calls.
	connMu sync.Mutex
	conn   net.Conn

	lastID       atomic.Int32
	requests     *sync.Map
	fetchTimeout time.Duration
	closing      atomic.Bool
}

type Request struct {
	ID     RequestID
	Result ResultType
	Done   chan struct{}
}
type StatInfo struct {
	Size    int64
	Mode    uint32
	GitType BinaryObjectType
}
type FetchStatRequest struct {
	Request
	Info StatInfo
}

func NewClient(socketPath string, fetchTimeout time.Duration) *Client {
	return &Client{
		socketPath:   socketPath,
		requests:     &sync.Map{},
		fetchTimeout: fetchTimeout,
	}
}

func (c *Client) IsClosing() bool {
	return c.closing.Load()
}
func (c *Client) Serve(ctx context.Context) error {
	if c.closing.Load() {
		return ErrClosedClient
	}

	// it's not safe to return until our spawned goroutine has the lock
	safeToReturn := make(chan struct{})
	defer close(safeToReturn)
	var once sync.Once

	go func() {
		defer func() {
			if p := recover(); p != nil {
				log.Printf("panic while handling remote messages %v", p)
			}
		}()

		loggedFailure := false
		for !c.closing.Load() {
			if err := ctx.Err(); err != nil {
				return
			}

			var connErr error
			var activeConn net.Conn
			// Limiting scope of writeLock.
			func() {
				c.writeLock.Lock()
				defer c.writeLock.Unlock()
				if c.closing.Load() {
					return
				}

				// We fall in here if the connection is broken.
				oldConn := c.swapConnWriteLocked(nil)
				if oldConn != nil {
					oldConn.Close()
				}

				once.Do(func() {
					safeToReturn <- struct{}{}
				})

				var conn net.Conn
				conn, connErr = net.Dial("unix", c.socketPath)
				if connErr == nil {
					c.setConnWriteLocked(conn)
					activeConn = conn
				}
			}()

			// This is done outside of the writeLock so that sendMessage calls aren't blocked,
			// but instead fail fast.
			if connErr != nil {
				if !c.closing.Load() {
					if !loggedFailure {
						log.Printf("could not connect to cacheman unix domain socket: %v", connErr)
						loggedFailure = true
					}
					select {
					case <-ctx.Done():
						return
					case <-time.After(1 * time.Second):
					}
					continue
				}
				return
			}

			if loggedFailure {
				log.Printf("re-connected to cacheman unix domain socket")
				loggedFailure = false
			}

			if err := c.serve(activeConn); err != nil && !c.closing.Load() {
				log.Printf("error handling messages: %v", err)
			}
		}
	}()

	<-safeToReturn
	return nil
}

func (c *Client) Close() error {
	if c.closing.Swap(true) {
		return ErrClosedClient
	}

	conn := c.getConn()
	if conn != nil {
		// This will cause all pending writes/reads to time out
		// This is done outside of write and read locks to avoid starvation
		if err := conn.SetDeadline(time.Now()); err != nil {
			return err
		}
	}

	// Grab write and read locks before snapshotting the
	// potentially changed c.conn and closing.
	c.writeLock.Lock()
	defer c.writeLock.Unlock()
	c.readLock.Lock()
	defer c.readLock.Unlock()

	conn = c.swapConnWriteLocked(nil)
	if conn != nil {
		return conn.Close()
	}
	return nil
}

func (c *Client) serve(reader io.Reader) error {
	c.readLock.Lock()
	defer c.readLock.Unlock()
	buf := make([]byte, 1024)
	p := 0
	for !c.closing.Load() {
		n, err := reader.Read(buf[p:])
		if err != nil {
			return err
		}
		q, err := c.processMessages(buf[:p+n])
		if err != nil {
			return err
		}
		copy(buf[0:], buf[q:p+n])
		p = p + n - q
	}
	return nil
}
func (c *Client) processMessages(buf []byte) (int, error) {
	i := 0

	for i+minResponseLength <= len(buf) {
		switch MessageType(buf[i]) {
		case FetchBlobRespType, FetchTreeRespType, FetchObjectRespType:
			id := RequestID(binary.LittleEndian.Uint32(buf[i+1 : i+5]))
			result := ResultType(buf[i+5])
			c.processFetchReply(id, result)
			i += minResponseLength
		case GetStatRespType:
			if i+fetchStatResponseLength > len(buf) {
				return i, nil
			}
			id := RequestID(binary.LittleEndian.Uint32(buf[i+1 : i+5]))
			result := ResultType(buf[i+5])
			size := binary.LittleEndian.Uint64(buf[i+6 : i+14])
			mode := binary.LittleEndian.Uint32(buf[i+14 : i+18])
			objType := BinaryObjectType(buf[i+18])
			info := StatInfo{
				Size:    int64(size),
				Mode:    mode,
				GitType: objType,
			}
			c.processFetchStatReply(id, result, info)
			i += fetchStatResponseLength
		default:
			return i, fmt.Errorf("received unknown message type 0x%02x", buf[i])
		}
	}
	return i, nil
}

func (c *Client) processFetchReply(id RequestID, result ResultType) {
	reqAny, ok := c.requests.Load(id)
	if !ok {
		log.Printf("received a response with id=0x%08x with no requests", id)
		return
	}
	req := reqAny.(*Request)
	req.Result = result
	close(req.Done)
}

func (c *Client) processFetchStatReply(id RequestID, result ResultType, info StatInfo) {
	reqAny, ok := c.requests.Load(id)
	if !ok {
		log.Printf("received a response with id=0x%08x with no requests", id)
		return
	}
	req := reqAny.(*FetchStatRequest)
	req.Result = result
	req.Info = info
	close(req.Done)
}

type WriterFunc func(w io.Writer) error

func (c *Client) sendMessage(msgType MessageType, writer WriterFunc) error {
	c.writeLock.Lock()
	defer c.writeLock.Unlock()

	conn := c.getConn()

	if conn == nil {
		return errors.New("client does not have an established connection")
	}

	_, err := conn.Write([]byte{byte(msgType)})
	if err != nil {
		return err
	}
	return writer(conn)
}

func (c *Client) SendNewObject(key hash.Hash, objType RequestObjectType) error {
	if c.closing.Load() {
		return ErrClosedClient
	}
	var msgType MessageType
	switch objType {
	case TypeBlob:
		msgType = NewBlobType
	case TypeTree:
		msgType = NewTreeType
	case TypeObject:
		msgType = NewObjectType
	default:
		return fmt.Errorf("unknown object type %02x", byte(objType))
	}

	return c.sendMessage(msgType, func(w io.Writer) error {
		_, err := key.Write(w)
		return err
	})
}
func (c *Client) FetchGitObjectRequest(key hash.Hash) (ResultType, error) {
	if c.closing.Load() {
		return ResultFailed, ErrClosedClient
	}
	id := RequestID(c.lastID.Add(1))
	msgType := FetchGitObjectType
	req := &Request{
		id, ResultWait, make(chan struct{}),
	}
	c.requests.Store(id, req)
	err := c.sendMessage(msgType, func(w io.Writer) error {
		return WriteFetchObjectReq(w, &FetchObjectReq{
			RequestID: id, Hash: key,
		})
	})
	if err != nil {
		return ResultFailed, err
	}

	select {
	case <-req.Done:
	case <-time.After(c.fetchTimeout):
		req.Result = ResultTimeout
	}

	c.requests.Delete(id)

	return req.Result, nil
}

func (c *Client) FetchObjectRequest(key hash.Hash, objType RequestObjectType) (ResultType, error) {
	if c.closing.Load() {
		return ResultFailed, ErrClosedClient
	}
	id := RequestID(c.lastID.Add(1))
	var msgType MessageType
	switch objType {
	case TypeBlob:
		msgType = FetchBlobType
	case TypeTree:
		msgType = FetchTreeType
	case TypeObject:
		msgType = FetchObjectType
	default:
		return ResultFailed, fmt.Errorf("unknown object type %02x", byte(objType))
	}
	req := &Request{
		id, ResultWait, make(chan struct{}),
	}
	c.requests.Store(id, req)
	err := c.sendMessage(msgType, func(w io.Writer) error {
		return WriteFetchObjectReq(w, &FetchObjectReq{
			RequestID: id, Hash: key,
		})
	})
	if err != nil {
		return ResultFailed, err
	}

	select {
	case <-req.Done:
	case <-time.After(c.fetchTimeout):
		req.Result = ResultTimeout
	}

	c.requests.Delete(id)

	return req.Result, nil
}
func (c *Client) FetchStatRequest(key hash.Hash) (StatInfo, error) {
	var ret StatInfo
	if c.closing.Load() {
		return ret, ErrClosedClient
	}
	id := RequestID(c.lastID.Add(1))
	req := &FetchStatRequest{
		Request{
			id, ResultWait, make(chan struct{}),
		},
		StatInfo{},
	}
	c.requests.Store(id, req)
	err := c.sendMessage(GetStatType, func(w io.Writer) error {
		return WriteFetchStatReq(w, &FetchStatReq{
			RequestID: id, Hash: key,
		})
	})
	if err != nil {
		return ret, err
	}

	select {
	case <-req.Done:
		ret = req.Info
	case <-time.After(c.fetchTimeout):
		err = fmt.Errorf("timed out waiting for result")
	}

	c.requests.Delete(id)

	return ret, err
}

func (c *Client) SendNotifyUse(key hash.Hash) error {
	if c.closing.Load() {
		return ErrClosedClient
	}
	return c.sendMessage(NotifyUseType, func(w io.Writer) error {
		if _, err := key.Write(w); err != nil {
			return err
		}
		return nil
	})

}

func (c *Client) getConn() net.Conn {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	return c.conn
}

func (c *Client) setConnWriteLocked(conn net.Conn) {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	c.conn = conn
}

func (c *Client) swapConnWriteLocked(conn net.Conn) net.Conn {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	old := c.conn
	c.conn = conn
	return old
}
