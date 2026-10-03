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

package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"syscall"

	"github.com/google/abfs/cas/cas/providers/cacheman"

	"github.com/google/abfs/shared/entities"
)

type CASCacheman interface {
	FetchGitObjectRequest(key entities.Hash) (cacheman.ResultType, error)
	FetchObjectRequest(key entities.Hash, objType cacheman.RequestObjectType) (cacheman.ResultType, error)
	FetchStatRequest(key entities.Hash) (entities.StatInfo, error)
	SendNewObject(key entities.Hash, objType cacheman.RequestObjectType) error
	SendNotifyUse(key entities.Hash) error

	Close() error
}

type RequestID uint32

type RequestObjectType byte

const (
	TypeBlob    RequestObjectType = 1
	TypeTree    RequestObjectType = 2
	TypeObject  RequestObjectType = 3
	TypeARI     RequestObjectType = 4
	TypeCASBlob RequestObjectType = 5
)

func (t RequestObjectType) String() string {
	switch t {
	case TypeBlob:
		return "blob"
	case TypeTree:
		return "tree"
	case TypeObject:
		return "object"
	case TypeARI:
		return "ari"
	case TypeCASBlob:
		return "cas_blob"
	default:
		return fmt.Sprintf("unknown object type %02x", byte(t))
	}
}

func ParseRequestType(t string) RequestObjectType {
	switch t {
	case "blob":
		return TypeBlob
	case "tree":
		return TypeTree
	case "ari":
		return TypeARI
	case "cas_blob":
		return TypeCASBlob
	default:
		return TypeObject
	}
}

type MessageType byte

const (
	FetchBlobType        MessageType = 0x01
	FetchBlobRespType    MessageType = 0x02
	FetchTreeType        MessageType = 0x03
	FetchTreeRespType    MessageType = 0x04
	FetchObjectType      MessageType = 0x05
	FetchObjectRespType  MessageType = 0x06
	NewBlobType          MessageType = 0x07
	NewTreeType          MessageType = 0x08
	NewObjectType        MessageType = 0x09
	FetchGitObjectType   MessageType = 0x0a
	GetStatType          MessageType = 0x0b
	GetStatRespType      MessageType = 0x0c
	VersionType          MessageType = 0x0d
	NotifyUseType        MessageType = 0x0e
	IdentityPutReqType   MessageType = 0x0f
	IdentityPutRespType  MessageType = 0x10
	IdentityGetReqType   MessageType = 0x11
	IdentityGetRespType  MessageType = 0x12
	MapResultsReqType    MessageType = 0x13
	MapResultsRespType   MessageType = 0x14
	PutInMapReqType      MessageType = 0x15
	PutInMapRespType     MessageType = 0x16
	FetchARIType         MessageType = 0x17
	FetchARIRespType     MessageType = 0x18
	InitDeeptreeReqType  MessageType = 0x19
	InitDeeptreeRespType MessageType = 0x1a
	CheckStatusType      MessageType = 0x1b
	CheckStatusRespType  MessageType = 0x1c
	FetchCASBlobType     MessageType = 0x1d
	FetchCASBlobRespType MessageType = 0x1e
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
	ErrResultWait   = errors.New("waiting for the request result")
	ErrResultFailed = errors.Join(syscall.ECONNREFUSED, errors.New("fetch failed"))
)

func (r ResultType) ToError() error {
	switch r {
	case ResultWait:
		return ErrResultWait
	case ResultSuccess:
		return nil
	case ResultNotFound:
		return syscall.ENOENT
	case ResultFailed:
		return ErrResultFailed
	case ResultTimeout:
		return syscall.EAGAIN
	default:
		return fmt.Errorf("unknown result %02x", int(r))
	}
}

type FetchObjectReq struct {
	RequestID RequestID
	Hash      entities.Hash
}

type FetchObjectResp struct {
	RequestID RequestID
	Result    ResultType
}

type FetchStatReq struct {
	RequestID RequestID
	Hash      entities.Hash
}

type FetchStatResp struct {
	RequestID  RequestID
	Result     ResultType
	Size       int64
	Mode       uint32
	ObjectType BinaryObjectType
}

type NewObjectMessage struct {
	Hash entities.Hash
}

func readHash(r io.Reader, hash *entities.Hash) error {
	_, err := io.ReadFull(r, hash[:])
	return err
}

func ReadFetchObjectReq(r io.Reader) (*FetchObjectReq, error) {
	m := new(FetchObjectReq)
	if err := binary.Read(r, binary.LittleEndian, &m.RequestID); err != nil {
		return nil, err
	}
	if err := readHash(r, &m.Hash); err != nil {
		return nil, err
	}
	return m, nil
}

func WriteFetchObjectReq(w io.Writer, req *FetchObjectReq) error {
	if err := binary.Write(w, binary.LittleEndian, &req.RequestID); err != nil {
		return err
	}
	if _, err := w.Write(req.Hash[:]); err != nil {
		return err
	}
	return nil
}

type FetchARIReq struct {
	RequestID RequestID
	ARI       string
}

func ReadFetchARIReq(r io.Reader) (*FetchARIReq, error) {
	m := new(FetchARIReq)
	if err := binary.Read(r, binary.LittleEndian, &m.RequestID); err != nil {
		return nil, err
	}
	var length uint16
	if err := binary.Read(r, binary.LittleEndian, &length); err != nil {
		return nil, err
	}
	if length == 0 || length > 4096 {
		return nil, fmt.Errorf("invalid ARI length %d", length)
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	m.ARI = string(buf)
	return m, nil
}

type FetchCASBlobReq struct {
	RequestID RequestID
	SHA256    entities.SHA256
	Size      int64
}

func ReadFetchCASBlobReq(r io.Reader) (*FetchCASBlobReq, error) {
	m := new(FetchCASBlobReq)
	if err := binary.Read(r, binary.LittleEndian, &m.RequestID); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(r, m.SHA256[:]); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.LittleEndian, &m.Size); err != nil {
		return nil, err
	}
	return m, nil
}

func WriteFetchCASBlobReq(w io.Writer, req *FetchCASBlobReq) error {
	buf := make([]byte, 0, 1+4+entities.SHA256Size+8)
	buf = append(buf, byte(FetchCASBlobType))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(req.RequestID))
	buf = append(buf, req.SHA256[:]...)
	buf = binary.LittleEndian.AppendUint64(buf, uint64(req.Size))
	_, err := w.Write(buf)
	return err
}

func ReadFetchCASBlobResp(r io.Reader) (*FetchObjectResp, error) {
	var opcode byte
	if err := binary.Read(r, binary.LittleEndian, &opcode); err != nil {
		return nil, err
	}
	if MessageType(opcode) != FetchCASBlobRespType {
		return nil, fmt.Errorf("unexpected opcode 0x%02x, expected 0x%02x", opcode, FetchCASBlobRespType)
	}
	m := new(FetchObjectResp)
	var reqID uint32
	if err := binary.Read(r, binary.LittleEndian, &reqID); err != nil {
		return nil, err
	}
	m.RequestID = RequestID(reqID)
	var result byte
	if err := binary.Read(r, binary.LittleEndian, &result); err != nil {
		return nil, err
	}
	m.Result = ResultType(result)
	return m, nil
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
	case TypeARI:
		buf = append(buf, byte(FetchARIRespType))
	case TypeCASBlob:
		buf = append(buf, byte(FetchCASBlobRespType))
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
	if _, err := w.Write(req.Hash[:]); err != nil {
		return err
	}
	return nil
}

func WriteFetchStatResp(w io.Writer, resp *FetchStatResp) error {
	buf := make([]byte, 0, 19)
	buf = append(buf, byte(GetStatRespType))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(resp.RequestID))
	buf = append(buf, byte(resp.Result))
	buf = binary.LittleEndian.AppendUint64(buf, uint64(resp.Size))
	// TODO: given that git only record a fixed number of mode
	// maybe we could use less byte to communicate
	buf = binary.LittleEndian.AppendUint32(buf, uint32(resp.Mode))
	buf = append(buf, byte(resp.ObjectType))
	_, err := w.Write(buf)
	return err
}

func ReadNewObjectMessage(r io.Reader) (*NewObjectMessage, error) {
	m := new(NewObjectMessage)
	if err := readHash(r, &m.Hash); err != nil {
		return nil, err
	}
	return m, nil
}
func ReadNotifyUseMessage(r io.Reader) (entities.Hash, error) {
	b := entities.Hash{}
	if err := binary.Read(r, binary.LittleEndian, &b); err != nil {
		return b, err
	}
	return b, nil
}

type UdsIdentityPutReq struct {
	RequestID RequestID
	SHA1      entities.Hash
	SHA256    [32]byte
	Size      int64
	Type      byte
}

type UdsIdentityGetReq struct {
	RequestID RequestID
	SHA1      entities.Hash
}

type UdsIdentityGetResp struct {
	RequestID RequestID
	Result    ResultType
	SHA256    [32]byte
	Size      int64
	Type      byte
}

type UdsMapResultsReq struct {
	RequestID RequestID
	SHA1      entities.Hash
}

type CheckStatusReq struct {
	RequestID RequestID
}

type CheckStatusResp struct {
	RequestID RequestID
	Result    ResultType
	ErrorCode int32
}

type UdsInitDeeptreeReq struct {
	RequestID RequestID
	SHA1      entities.Hash
}

type UdsInitDeeptreeResp struct {
	RequestID RequestID
	Result    ResultType
	SHA1      entities.Hash
}

func ReadUdsIdentityPutReq(r io.Reader) (*UdsIdentityPutReq, error) {
	m := new(UdsIdentityPutReq)
	if err := binary.Read(r, binary.LittleEndian, &m.RequestID); err != nil {
		return nil, err
	}
	if err := readHash(r, &m.SHA1); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(r, m.SHA256[:]); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.LittleEndian, &m.Size); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.LittleEndian, &m.Type); err != nil {
		return nil, err
	}
	return m, nil
}

func WriteSimpleUdsResp(w io.Writer, respType MessageType, reqID RequestID, result ResultType) error {
	buf := make([]byte, 0, 6)
	buf = append(buf, byte(respType))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(reqID))
	buf = append(buf, byte(result))
	_, err := w.Write(buf)
	return err
}

func ReadUdsIdentityGetReq(r io.Reader) (*UdsIdentityGetReq, error) {
	m := new(UdsIdentityGetReq)
	if err := binary.Read(r, binary.LittleEndian, &m.RequestID); err != nil {
		return nil, err
	}
	if err := readHash(r, &m.SHA1); err != nil {
		return nil, err
	}
	return m, nil
}

func WriteUdsIdentityGetResp(w io.Writer, resp *UdsIdentityGetResp) error {
	if resp.Result != ResultSuccess {
		return WriteSimpleUdsResp(w, IdentityGetRespType, resp.RequestID, resp.Result)
	}

	buf := make([]byte, 0, 47)
	buf = append(buf, byte(IdentityGetRespType))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(resp.RequestID))
	buf = append(buf, byte(resp.Result))
	buf = append(buf, resp.SHA256[:]...)
	buf = binary.LittleEndian.AppendUint64(buf, uint64(resp.Size))
	buf = append(buf, resp.Type)
	_, err := w.Write(buf)
	return err
}

func ReadUdsMapResultsReq(r io.Reader) (*UdsMapResultsReq, error) {
	m := new(UdsMapResultsReq)
	if err := binary.Read(r, binary.LittleEndian, &m.RequestID); err != nil {
		return nil, err
	}
	if err := readHash(r, &m.SHA1); err != nil {
		return nil, err
	}
	return m, nil
}

func WriteUdsMapResultsResp(w io.Writer, reqID RequestID, result ResultType, hashes []entities.Hash) error {
	if result != ResultSuccess {
		return WriteSimpleUdsResp(w, MapResultsRespType, reqID, result)
	}

	const maxHashesCount = 65535
	count := len(hashes)
	if count > maxHashesCount {
		if err := WriteSimpleUdsResp(w, MapResultsRespType, reqID, ResultFailed); err != nil {
			return fmt.Errorf("too many hashes: %d exceeds maximum count of %d (failed to write response: %w)", count, maxHashesCount, err)
		}
		return fmt.Errorf("too many hashes: %d exceeds maximum count of %d", count, maxHashesCount)
	}

	buf := make([]byte, 0, 8+count*20)
	buf = append(buf, byte(MapResultsRespType))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(reqID))
	buf = append(buf, byte(result))
	buf = binary.LittleEndian.AppendUint16(buf, uint16(count))
	for _, h := range hashes {
		buf = append(buf, h[:]...)
	}
	_, err := w.Write(buf)
	return err
}

func WriteUdsIdentityPutReq(w io.Writer, req *UdsIdentityPutReq) error {
	buf := make([]byte, 0, 66)
	buf = append(buf, byte(IdentityPutReqType))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(req.RequestID))
	buf = append(buf, req.SHA1[:]...)
	buf = append(buf, req.SHA256[:]...)
	buf = binary.LittleEndian.AppendUint64(buf, uint64(req.Size))
	buf = append(buf, req.Type)
	_, err := w.Write(buf)
	return err
}

func WriteUdsIdentityGetReq(w io.Writer, req *UdsIdentityGetReq) error {
	buf := make([]byte, 0, 25)
	buf = append(buf, byte(IdentityGetReqType))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(req.RequestID))
	buf = append(buf, req.SHA1[:]...)
	_, err := w.Write(buf)
	return err
}

func WriteUdsMapResultsReq(w io.Writer, req *UdsMapResultsReq) error {
	buf := make([]byte, 0, 25)
	buf = append(buf, byte(MapResultsReqType))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(req.RequestID))
	buf = append(buf, req.SHA1[:]...)
	_, err := w.Write(buf)
	return err
}

type UdsPutInMapReq struct {
	RequestID RequestID
	From      entities.Hash
	To        entities.Hash
	LocalOnly byte
}

func ReadUdsPutInMapReq(r io.Reader) (*UdsPutInMapReq, error) {
	m := new(UdsPutInMapReq)
	if err := binary.Read(r, binary.LittleEndian, &m.RequestID); err != nil {
		return nil, err
	}
	if err := readHash(r, &m.From); err != nil {
		return nil, err
	}
	if err := readHash(r, &m.To); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.LittleEndian, &m.LocalOnly); err != nil {
		return nil, err
	}
	return m, nil
}

func WriteUdsPutInMapReq(w io.Writer, req *UdsPutInMapReq) error {
	buf := make([]byte, 0, 46)
	buf = append(buf, byte(PutInMapReqType))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(req.RequestID))
	buf = append(buf, req.From[:]...)
	buf = append(buf, req.To[:]...)
	buf = append(buf, req.LocalOnly)
	_, err := w.Write(buf)
	return err
}

func ReadUdsInitDeeptreeReq(r io.Reader) (*UdsInitDeeptreeReq, error) {
	m := new(UdsInitDeeptreeReq)
	if err := binary.Read(r, binary.LittleEndian, &m.RequestID); err != nil {
		return nil, err
	}
	if err := readHash(r, &m.SHA1); err != nil {
		return nil, err
	}
	return m, nil
}

func WriteUdsInitDeeptreeReq(w io.Writer, req *UdsInitDeeptreeReq) error {
	buf := make([]byte, 0, 25)
	buf = append(buf, byte(InitDeeptreeReqType))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(req.RequestID))
	buf = append(buf, req.SHA1[:]...)
	_, err := w.Write(buf)
	return err
}

func WriteUdsInitDeeptreeResp(w io.Writer, reqID RequestID, result ResultType, hash entities.Hash) error {
	if result != ResultSuccess {
		return WriteSimpleUdsResp(w, InitDeeptreeRespType, reqID, result)
	}
	buf := make([]byte, 0, 26)
	buf = append(buf, byte(InitDeeptreeRespType))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(reqID))
	buf = append(buf, byte(result))
	buf = append(buf, hash[:]...)
	_, err := w.Write(buf)
	return err
}

func ReadCheckStatusReq(r io.Reader) (*CheckStatusReq, error) {
	m := new(CheckStatusReq)
	if err := binary.Read(r, binary.LittleEndian, &m.RequestID); err != nil {
		return nil, err
	}
	return m, nil
}

func WriteCheckStatusReq(w io.Writer, reqID RequestID) error {
	buf := make([]byte, 0, 5)
	buf = append(buf, byte(CheckStatusType))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(reqID))
	_, err := w.Write(buf)
	return err
}

func ReadCheckStatusResp(r io.Reader) (*CheckStatusResp, error) {
	var opcode byte
	if err := binary.Read(r, binary.LittleEndian, &opcode); err != nil {
		return nil, err
	}
	if MessageType(opcode) != CheckStatusRespType {
		return nil, fmt.Errorf("unexpected opcode 0x%02x, expected 0x%02x", opcode, CheckStatusRespType)
	}
	m := new(CheckStatusResp)
	if err := binary.Read(r, binary.LittleEndian, &m.RequestID); err != nil {
		return nil, err
	}
	var res byte
	if err := binary.Read(r, binary.LittleEndian, &res); err != nil {
		return nil, err
	}
	m.Result = ResultType(res)
	if err := binary.Read(r, binary.LittleEndian, &m.ErrorCode); err != nil {
		return nil, err
	}
	return m, nil
}

func WriteCheckStatusResp(w io.Writer, reqID RequestID, result ResultType, errCode int32) error {
	buf := make([]byte, 0, 10)
	buf = append(buf, byte(CheckStatusRespType))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(reqID))
	buf = append(buf, byte(result))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(errCode))
	_, err := w.Write(buf)
	return err
}
