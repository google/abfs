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
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"syscall"

	"github.com/google/abfs/cas/hash"
)

type RequestID uint32

type RequestObjectType byte

const (
	TypeBlob   RequestObjectType = 1
	TypeTree   RequestObjectType = 2
	TypeObject RequestObjectType = 3
)

func (t RequestObjectType) String() string {
	switch t {
	case TypeBlob:
		return "blob"
	case TypeTree:
		return "tree"
	case TypeObject:
		return "object"
	default:
		return fmt.Sprintf("unknown object type %02x", byte(t))
	}
}

type MessageType byte

const (
	FetchBlobType       MessageType = 0x01
	FetchBlobRespType   MessageType = 0x02
	FetchTreeType       MessageType = 0x03
	FetchTreeRespType   MessageType = 0x04
	FetchObjectType     MessageType = 0x05
	FetchObjectRespType MessageType = 0x06
	NewBlobType         MessageType = 0x07
	NewTreeType         MessageType = 0x08
	NewObjectType       MessageType = 0x09
	FetchGitObjectType  MessageType = 0x0a
	GetStatType         MessageType = 0x0b
	GetStatRespType     MessageType = 0x0c
	VersionType         MessageType = 0x0d
	NotifyUseType       MessageType = 0x0e
)

type ResultType byte

const (
	ResultWait     ResultType = 0xff
	ResultSuccess  ResultType = 0x01
	ResultNotFound ResultType = 0x02
	ResultFailed   ResultType = 0x03
	ResultTimeout  ResultType = 0x04
)

type BinaryObjectType byte

const (
	BinaryBlob   BinaryObjectType = 0x01
	BinaryObject BinaryObjectType = 0x02
)

func (r ResultType) String() string {
	switch r {
	case ResultWait:
		return "waiting"
	case ResultSuccess:
		return "succeeded"
	case ResultNotFound:
		return "not-found"
	case ResultFailed:
		return "failed"
	case ResultTimeout:
		return "timed-out"
	default:
		return fmt.Sprintf("unknown result %02x", int(r))
	}
}

var (
	ResultWaitError   = errors.New("waiting for the request result")
	ResultFailedError = errors.Join(syscall.ECONNREFUSED, errors.New("fetch failed"))
)

func (r ResultType) ToError() error {
	switch r {
	case ResultWait:
		return ResultWaitError
	case ResultSuccess:
		return nil
	case ResultNotFound:
		return syscall.ENOENT
	case ResultFailed:
		return ResultFailedError
	case ResultTimeout:
		return syscall.EAGAIN
	default:
		return fmt.Errorf("unknown result %02x", int(r))
	}
}

type FetchObjectReq struct {
	RequestID RequestID
	Hash      hash.Hash
}

type FetchObjectResp struct {
	RequestID RequestID
	Result    ResultType
}
type FetchStatReq struct {
	RequestID RequestID
	Hash      hash.Hash
}

type FetchStatResp struct {
	RequestID  RequestID
	Result     ResultType
	Size       int64
	Mode       uint32
	ObjectType BinaryObjectType
}

/*
	func readHash(r io.Reader, hash hash.Hash) error {
		_, err := hash.Read(r)
		return err
	}

	func ReadFetchObjectReq(r io.Reader) (*FetchObjectReq, error) {
		m := new(FetchObjectReq)
		if err := binary.Read(r, binary.LittleEndian, &m.RequestID); err != nil {
			return nil, err
		}
		if err := readHash(r, m.Hash); err != nil {
			return nil, err
		}
		return m, nil
	}
*/
func WriteFetchObjectReq(w io.Writer, req *FetchObjectReq) error {
	if err := binary.Write(w, binary.LittleEndian, &req.RequestID); err != nil {
		return err
	}
	if _, err := req.Hash.Write(w); err != nil {
		return err
	}
	return nil
}

func WriteFetchObjectResp(w io.Writer, resp *FetchObjectResp, objType RequestObjectType) error {
	buf := make([]byte, 0, 6)
	switch objType {
	case TypeBlob:
		buf = append(buf, byte(FetchBlobRespType))
	case TypeTree:
		buf = append(buf, byte(FetchTreeRespType))
	case TypeObject:
		buf = append(buf, byte(FetchObjectRespType))
	default:
		return fmt.Errorf("unknown object type %02x", objType)
	}
	buf = binary.LittleEndian.AppendUint32(buf, uint32(resp.RequestID))
	buf = append(buf, byte(resp.Result))
	_, err := w.Write(buf)
	return err
}
func WriteFetchStatReq(w io.Writer, req *FetchStatReq) error {
	if err := binary.Write(w, binary.LittleEndian, &req.RequestID); err != nil {
		return err
	}
	if _, err := req.Hash.Write(w); err != nil {
		return err
	}
	return nil
}
