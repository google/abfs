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

package server

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/google/abfs/shared/cacheman/protocol"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects/gitobjects"
	rbemodel "github.com/google/abfs/shared/rbe/model"
	"github.com/google/abfs/shared/version"
)

var ErrProtocol = errors.New("cacheman binary protocol error")

type Connection struct {
	server  *CacheMan
	conn    net.Conn
	lock    sync.Mutex
	closed  bool
	printer Printer
	ctx     context.Context
	cancel  context.CancelFunc
}

func newConnection(c *CacheMan, conn net.Conn) *Connection {
	ctx, cancel := context.WithCancel(context.Background())
	return &Connection{
		server:  c,
		conn:    conn,
		printer: c.printer,
		ctx:     ctx,
		cancel:  cancel,
	}
}

func (c *Connection) Serve() error {
	buf := make([]byte, 1)
	for {
		n, err := c.conn.Read(buf)
		if n == 0 || err == io.EOF {
			return nil
		}
		if err != nil {
			// Non-recoverable
			return err
		}

		switch protocol.MessageType(buf[0]) {
		case protocol.FetchBlobType:
			err = c.handleFetchObject(protocol.TypeBlob)
		case protocol.FetchTreeType:
			err = c.handleFetchObject(protocol.TypeTree)
		case protocol.FetchObjectType:
			err = c.handleFetchObject(protocol.TypeObject)
		case protocol.FetchARIType:
			err = c.handleFetchARI()
		case protocol.FetchCASBlobType:
			err = c.handleFetchCASBlob()
		case protocol.FetchGitObjectType:
			err = c.handlePrefetchGitObject(protocol.TypeObject)
		case protocol.NewBlobType:
			err = c.handleNewObject(protocol.TypeBlob)
		case protocol.NewTreeType:
			err = c.handleNewObject(protocol.TypeTree)
		case protocol.NewObjectType:
			err = c.handleNewObject(protocol.TypeObject)
		case protocol.GetStatType:
			err = c.handleGetStat()
		case protocol.VersionType:
			err = c.handleVersionReq()
		case protocol.NotifyUseType:
			err = c.handleNotifyUse()
		case protocol.IdentityPutReqType:
			err = c.handleIdentityPut()
		case protocol.IdentityGetReqType:
			err = c.handleIdentityGet()
		case protocol.MapResultsReqType:
			err = c.handleMapResults()
		case protocol.PutInMapReqType:
			err = c.handlePutInMap()
		case protocol.CheckStatusType:
			err = c.handleCheckStatus()
		case protocol.InitDeeptreeReqType:
			err = c.handleInitDeeptree()
		default:
			err = fmt.Errorf("unknown message type %x", buf[0])
		}

		if errors.Is(err, ErrProtocol) {
			return err
		} else if err != nil {
			c.printer.Printf("Serve() error: %v. Attempting to continue.\n", err)
		}
	}
}

func (c *Connection) Close() error {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if c.cancel != nil {
		c.cancel()
	}
	return c.conn.Close()
}

func (c *Connection) handlePrefetchGitObject(objType protocol.RequestObjectType) error {
	if !c.server.config.GitPrefetching {
		return c.handleFetchObject(protocol.TypeObject)
	}

	req, err := protocol.ReadFetchObjectReq(c.conn)
	if err != nil {
		return err
	}

	if debug {
		c.printer.Printf("Received request #0x%08x to fetch object (type=%s) for %s\n", req.RequestID, objType, req.Hash)
	}

	c.server.PrefetchGitObject(req.Hash, func(err error) {
		result := ResultSuccess
		if err != nil {
			c.printer.Printf("Failed to fetch #0x%08x (type=%s) for %s: %v\n", req.RequestID, objType, req.Hash, err)
			if entities.IsNotExist(err) {
				result = ResultNotFound
			} else {
				result = ResultFailed
			}
		}
		resp := &protocol.FetchObjectResp{
			RequestID: req.RequestID,
			Result:    result,
		}

		c.lock.Lock()
		defer c.lock.Unlock()

		if err := protocol.WriteFetchObjectResp(c.conn, resp, objType); err != nil {
			c.printer.Printf("error writing response: %v\n", err)
		}
		if debug {
			c.printer.Printf("sent the 0x%02d as result of 0x%08x request\n", resp.Result, resp.RequestID)
		}
	})

	return nil
}

func (c *Connection) handleGetStat() error {
	req, err := protocol.ReadFetchObjectReq(c.conn)
	if err != nil {
		return err
	}

	if debug {
		c.printer.Printf("Received request #0x%08x to fetch stat for %s\n", req.RequestID, req.Hash)
	}

	makeResp := func(result protocol.ResultType) {
		var stat entities.StatInfo
		if result == ResultSuccess {
			stat, err = c.server.GetStat(req.Hash)
			if err != nil {
				c.printer.Printf("Failed to get stat for %s after fetchcall succeed: %v\n", req.Hash, err)
				result = ResultFailed
			}
		}
		var t protocol.BinaryObjectType
		switch stat.GitType {
		case entities.ObjectTypeBlob:
			t = protocol.BinaryBlob
		case gitobjects.ObjectTypeTag, gitobjects.ObjectTypeCommit, gitobjects.ObjectTypeDir:
			t = protocol.BinaryObject
		}
		resp := &protocol.FetchStatResp{
			RequestID:  req.RequestID,
			Result:     result,
			Size:       stat.Size,
			Mode:       uint32(stat.Mode),
			ObjectType: t,
		}

		c.lock.Lock()
		defer c.lock.Unlock()

		if err := protocol.WriteFetchStatResp(c.conn, resp); err != nil {
			c.printer.Printf("error writing response: %v\n", err)
		}
		if debug {
			c.printer.Printf("sent the 0x%02d as result of 0x%08x request\n", resp.Result, resp.RequestID)
		}

	}

	// Currently only cacheman itself holding statinfo
	// Return directly without queueing if it is already stored
	if _, err := c.server.GetStat(req.Hash); err == nil {
		makeResp(ResultSuccess)
		return nil
	}

	cbFunc := func(err error) {
		result := ResultSuccess
		if err != nil {
			c.printer.Printf("Failed to fetch stat #0x%08x for %s: %v\n", req.RequestID, req.Hash, err)
			if entities.IsNotExist(err) {
				result = ResultNotFound
			} else {
				result = ResultFailed
			}
		}
		makeResp(result)
	}

	if !c.server.config.GitPrefetching {
		c.server.FetchObject(req.Hash, protocol.TypeObject, defaultFetchPriority, cbFunc)
	} else {
		c.server.PrefetchGitObject(req.Hash, cbFunc)
	}

	return nil
}

func (c *Connection) handleFetchObject(objType protocol.RequestObjectType) error {
	req, err := protocol.ReadFetchObjectReq(c.conn)
	if err != nil {
		return fmt.Errorf("%w: ReadFetchObjectReq failed: %v", ErrProtocol, err)
	}

	if debug {
		c.printer.Printf("Received request #0x%08x to fetch object (type=%s) for %s\n", req.RequestID, objType, req.Hash)
	}

	c.server.FetchObject(req.Hash, objType, defaultFetchPriority, func(err error) {
		result := ResultSuccess
		if err != nil {
			c.printer.Printf("Failed to fetch #0x%08x (type=%s) for %s: %v\n", req.RequestID, objType, req.Hash, err)
			if entities.IsNotExist(err) {
				result = ResultNotFound
			} else {
				result = ResultFailed
			}
		}
		resp := &protocol.FetchObjectResp{
			RequestID: req.RequestID,
			Result:    result,
		}

		c.lock.Lock()
		defer c.lock.Unlock()

		if err := protocol.WriteFetchObjectResp(c.conn, resp, objType); err != nil {
			c.printer.Printf("error writing response: %v\n", err)
		}
		if debug {
			c.printer.Printf("sent the 0x%02d as result of 0x%08x request\n", resp.Result, resp.RequestID)
		}
	})

	return nil
}

func (c *Connection) handleFetchARI() error {
	req, err := protocol.ReadFetchARIReq(c.conn)
	if err != nil {
		return fmt.Errorf("%w: ReadFetchARIReq failed: %v", ErrProtocol, err)
	}

	if debug {
		c.printer.Printf("Received request #0x%08x to fetch ARI for %s\n", req.RequestID, req.ARI)
	}

	c.server.FetchARI(req.ARI, defaultFetchPriority, func(err error) {
		result := ResultSuccess
		if err != nil {
			c.printer.Printf("Failed to fetch #0x%08x ARI for %s: %v\n", req.RequestID, req.ARI, err)
			if entities.IsNotExist(err) {
				result = ResultNotFound
			} else {
				result = ResultFailed
			}
		}
		resp := &protocol.FetchObjectResp{
			RequestID: req.RequestID,
			Result:    result,
		}

		c.lock.Lock()
		defer c.lock.Unlock()

		if err := protocol.WriteFetchObjectResp(c.conn, resp, protocol.TypeARI); err != nil {
			c.printer.Printf("error writing response: %v\n", err)
		}
		if debug {
			c.printer.Printf("sent the 0x%02d as result of 0x%08x request\n", resp.Result, resp.RequestID)
		}
	})

	return nil
}

func (c *Connection) handleFetchCASBlob() error {
	req, err := protocol.ReadFetchCASBlobReq(c.conn)
	if err != nil {
		return fmt.Errorf("%w: ReadFetchCASBlobReq failed: %v", ErrProtocol, err)
	}

	sha256Hex := req.SHA256.String()
	if debug {
		c.printer.Printf("Received request #0x%08x to fetch CAS blob %s (size %d)\n", req.RequestID, sha256Hex, req.Size)
	}

	c.server.FetchCASBlob(sha256Hex, req.Size, defaultFetchPriority, func(err error) {
		result := ResultSuccess
		if err != nil {
			c.printer.Printf("Failed to fetch #0x%08x CAS blob %s: %v\n", req.RequestID, sha256Hex, err)
			if entities.IsNotExist(err) {
				result = ResultNotFound
			} else {
				result = ResultFailed
			}
		}
		resp := &protocol.FetchObjectResp{
			RequestID: req.RequestID,
			Result:    result,
		}

		c.lock.Lock()
		defer c.lock.Unlock()

		if err := protocol.WriteFetchObjectResp(c.conn, resp, protocol.TypeCASBlob); err != nil {
			c.printer.Printf("error writing response: %v\n", err)
		}
		if debug {
			c.printer.Printf("sent 0x%02d as result of 0x%08x request\n", resp.Result, resp.RequestID)
		}
	})

	return nil
}

func (c *Connection) handleNewObject(objType protocol.RequestObjectType) error {
	m, err := protocol.ReadNewObjectMessage(c.conn)
	if err != nil {
		return fmt.Errorf("%w: ReadNewObjectMesesage failed: %v", ErrProtocol, err)
	}
	if debug {
		c.printer.Printf("Received a notification for a new object (type=%s) for %s\n", objType, m)
	}
	// Offload indexing and eager-push to an asynchronous background worker
	// queue to prevent head-of-line blocking on the connection reader loop.
	c.server.EnqueueNewObject(m.Hash, objType)
	return nil
}

// handleVersionReq sends the version information after a 1-byte header
// containing the version length.
func (c *Connection) handleVersionReq() error {
	verBytes := []byte(version.Version())
	if len(verBytes) > 256 {
		return fmt.Errorf("%w: version %q is larger than 256 bytes", ErrProtocol, version.Version())
	}
	length := byte(len(verBytes))

	c.lock.Lock()
	defer c.lock.Unlock()

	if _, err := c.conn.Write([]byte{length}); err != nil {
		return err
	}
	if _, err := c.conn.Write(verBytes); err != nil {
		return err
	}

	return nil
}

func (c *Connection) handleNotifyUse() error {
	m, err := protocol.ReadNotifyUseMessage(c.conn)
	if err != nil {
		return fmt.Errorf("%w: ReadNotifyUseMessage failed: %v", ErrProtocol, err)
	}

	// Buffer touch notification into the in-memory aggregator to avoid
	// committing individual transactions synchronously on the socket reader.
	c.server.NotifyUse(m)
	return nil
}

// handleIdentityPut processes a request to store/register a mapping from a Git SHA-1 hash
// to its corresponding RBE digest (SHA-256, size, and node type) in Cacheman's identity store.
func (c *Connection) handleIdentityPut() error {
	req, err := protocol.ReadUdsIdentityPutReq(c.conn)
	if err != nil {
		return fmt.Errorf("%w: ReadUdsIdentityPutReq failed: %v", ErrProtocol, err)
	}

	if debug {
		c.printer.Printf("Received request #[%08x] to put identity for [%s]\n", req.RequestID, req.SHA1)
	}

	result := protocol.ResultSuccess
	if c.server.identityStore == nil {
		c.printer.Printf("Failed to put identity #[%08x]: identity store not enabled\n", req.RequestID)
		result = protocol.ResultFailed
	} else {
		sha256Hex := hex.EncodeToString(req.SHA256[:])
		err = c.server.identityStore.Put(req.SHA1, rbemodel.Node{
			Digest: rbemodel.Digest{
				SHA256: sha256Hex,
				Size:   req.Size,
			},
			Type: rbemodel.NodeType(req.Type),
		})
		if err != nil {
			c.printer.Printf("Failed to put identity #[%08x]: [%v]\n", req.RequestID, err)
			result = protocol.ResultFailed
		}
	}

	c.lock.Lock()
	defer c.lock.Unlock()
	if err := protocol.WriteSimpleUdsResp(c.conn, protocol.IdentityPutRespType, req.RequestID, result); err != nil {
		c.printer.Printf("error writing response: [%v]\n", err)
	}
	return nil
}

// handleIdentityGet processes a request to retrieve the stored RBE digest mapping (if any)
// associated with a given Git SHA-1 hash from Cacheman's identity store.
func (c *Connection) handleIdentityGet() error {
	req, err := protocol.ReadUdsIdentityGetReq(c.conn)
	if err != nil {
		return fmt.Errorf("%w: ReadUdsIdentityGetReq failed: %v", ErrProtocol, err)
	}

	if debug {
		c.printer.Printf("Received request #[%08x] to get identity for [%s]\n", req.RequestID, req.SHA1)
	}

	resp := &protocol.UdsIdentityGetResp{
		RequestID: req.RequestID,
		Result:    protocol.ResultSuccess,
	}

	if c.server.identityStore == nil {
		c.printer.Printf("Failed to get identity #[%08x]: identity store not enabled\n", req.RequestID)
		resp.Result = protocol.ResultFailed
	} else {
		node, found, err := c.server.identityStore.Get(req.SHA1)
		if err != nil {
			c.printer.Printf("Failed to get identity #[%08x]: [%v]\n", req.RequestID, err)
			resp.Result = protocol.ResultFailed
		} else if !found {
			resp.Result = protocol.ResultNotFound
		} else {
			sha256Bytes, err := hex.DecodeString(node.Digest.SHA256)
			if err != nil || len(sha256Bytes) != 32 {
				c.printer.Printf("Failed to decode SHA256 for identity #[%08x]: [%v]\n", req.RequestID, err)
				resp.Result = protocol.ResultFailed
			} else {
				copy(resp.SHA256[:], sha256Bytes)
				resp.Size = node.Digest.Size
				resp.Type = byte(node.Type)
			}
		}
	}

	c.lock.Lock()
	defer c.lock.Unlock()
	if err := protocol.WriteUdsIdentityGetResp(c.conn, resp); err != nil {
		c.printer.Printf("error writing response: [%v]\n", err)
	}
	return nil
}

// handleMapResults processes a query request to retrieve the target mapped output hashes
// for a given source Git SHA-1 hash from Cacheman's maps store.
func (c *Connection) handleMapResults() error {
	req, err := protocol.ReadUdsMapResultsReq(c.conn)
	if err != nil {
		return fmt.Errorf("%w: ReadUdsMapResultsReq failed: %v", ErrProtocol, err)
	}

	if debug {
		c.printer.Printf("Received request #[%08x] to get map results for [%s]\n", req.RequestID, req.SHA1)
	}

	result := protocol.ResultSuccess
	var hashes []entities.Hash

	if c.server.maps == nil {
		c.printer.Printf("Failed to get map results #[%08x]: maps store not enabled\n", req.RequestID)
		result = protocol.ResultFailed
	} else {
		var err error
		hashes, err = c.server.maps.Get(req.SHA1)
		if err != nil {
			c.printer.Printf("Failed to get map results #[%08x]: [%v]\n", req.RequestID, err)
			result = protocol.ResultFailed
		} else if len(hashes) == 0 {
			result = protocol.ResultNotFound
		}
	}

	c.lock.Lock()
	defer c.lock.Unlock()
	if err := protocol.WriteUdsMapResultsResp(c.conn, req.RequestID, result, hashes); err != nil {
		c.printer.Printf("error writing response: [%v]\n", err)
	}
	return nil
}

// handlePutInMap processes a request to store a new mapping from an input hash to an output
// hash in Cacheman's maps store (always persisted locally).
func (c *Connection) handlePutInMap() error {
	req, err := protocol.ReadUdsPutInMapReq(c.conn)
	if err != nil {
		return fmt.Errorf("%w: ReadUdsPutInMapReq failed: %v", ErrProtocol, err)
	}

	if debug {
		c.printer.Printf("Received request #[%08x] to put map from [%s] to [%s] (localOnly: [%d])\n", req.RequestID, req.From, req.To, req.LocalOnly)
	}

	result := protocol.ResultSuccess

	if c.server.maps == nil {
		c.printer.Printf("Failed to put map results #[%08x]: maps store not enabled\n", req.RequestID)
		result = protocol.ResultFailed
	} else {
		c.server.NewMap(req.From, req.To /* localonly= */, req.LocalOnly != 0)
	}

	c.lock.Lock()
	defer c.lock.Unlock()
	if err := protocol.WriteSimpleUdsResp(c.conn, protocol.PutInMapRespType, req.RequestID, result); err != nil {
		c.printer.Printf("error writing response: [%v]\n", err)
	}
	return nil
}

// handleInitDeeptree reads the request off the connection and then performs the
// (potentially very long) fetch + DeepTree compilation asynchronously.
//
// The fetch + compile must not run inline: it would block Connection.Serve()'s
// read loop for its entire duration, leaving the next request's bytes unread in
// the socket buffer. Because GitWS multiplexes all of its requests over a
// single UDS connection, that serializes every concurrent InitDeeptree call,
// and a client-side N-way DeepTree fan-out ends up executing strictly one shard
// at a time - making hydration sum-bound on this server instead of bound by the
// largest shard. handleFetchObject uses the same async pattern.
//
// Only the socket read stays on the read loop; it must, to keep request framing
// ordered. c.lock already serializes the response write on the shared socket.
func (c *Connection) handleInitDeeptree() error {
	req, err := protocol.ReadUdsInitDeeptreeReq(c.conn)
	if err != nil {
		return fmt.Errorf("%w: ReadUdsInitDeeptreeReq failed: %v", ErrProtocol, err)
	}

	if debug {
		c.printer.Printf("Received request #[%08x] to init deeptree for [%s]\n", req.RequestID, req.SHA1)
	}

	go c.initDeeptreeAsync(req)
	return nil
}

// initDeeptreeAsync resolves (and if necessary compiles) the DeepTree for the
// requested tree and writes the response.
//
// The resolution itself belongs to the server, not to this connection: its
// result is shared between every caller coalesced into the same singleflight,
// so it must not be scoped to one client. See (*CacheMan).resolveDeeptree.
//
// A consequence is that if this connection drops mid-flight, this goroutine
// stays parked in the singleflight until the shared compile finishes (or the
// server shuts down), and then fails its response write benignly. It does not
// cancel the compile, which the other callers still need.
func (c *Connection) initDeeptreeAsync(req *protocol.UdsInitDeeptreeReq) {
	defer c.server.logPanic()

	result := protocol.ResultSuccess
	deeptreeHash, err := c.server.resolveDeeptree(req.SHA1)
	if err != nil {
		c.printer.Printf("Failed to init deeptree #[%08x] for [%s]: %v\n", req.RequestID, req.SHA1, err)
		result = protocol.ResultFailed
		deeptreeHash = entities.Hash{}
	}

	c.lock.Lock()
	defer c.lock.Unlock()
	if err := protocol.WriteUdsInitDeeptreeResp(c.conn, req.RequestID, result, deeptreeHash); err != nil {
		c.printer.Printf("error writing response: [%v]\n", err)
	}
}

func (c *Connection) handleCheckStatus() error {
	req, err := protocol.ReadCheckStatusReq(c.conn)
	if err != nil {
		return fmt.Errorf("%w: ReadCheckStatusReq failed: %v", ErrProtocol, err)
	}

	errCode := c.server.HealthStatus()
	result := protocol.ResultSuccess
	if errCode != 0 {
		result = protocol.ResultFailed
	}

	c.lock.Lock()
	defer c.lock.Unlock()
	if err := protocol.WriteCheckStatusResp(c.conn, req.RequestID, result, errCode); err != nil {
		return fmt.Errorf("%w: WriteCheckStatusResp failed: %v", ErrProtocol, err)
	}
	return nil
}
