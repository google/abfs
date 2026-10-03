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

package git

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strconv"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/hash"
)

var (
	ErrUnknownType = errors.New("unknown type")
	ErrObjectParse = errors.New("failed to parse the object")
	ErrNoDelim     = errors.New("failed to find delimiter")
)

type Mode uint32

const (
	ModeDir         Mode = 0040000
	ModeRegular     Mode = 0100644
	ModeExecutable  Mode = 0100755
	ModeSymLink     Mode = 0120000
	ModeFifo        Mode = 0140000
	ModeSubModule   Mode = 0160000
	CASModeSocket   Mode = 0170000 // Not a proper GIT type
	CASModeDeepTree Mode = 0210000 // Not a proper GIT type
)

const (
	treeMetaTag  = "tree"
	deepTreeName = "deeptree"
)

type Objects struct {
	Store
}

func NewObjects(path string, hloc HashToFileLoc, h hash.Hasher) *Objects {
	return &Objects{Store: newStore(path, hloc, h)}
}

// SaveDirEntries save the directory entries via the git tree and extended size meta data.
// The git tree object is used to store the directory entries this data is hashed with
// the configured hasher.
// The tree is saved via its hash under the objects directory
// The entries sizes are stored into a separate data structure as this information is not
// part of the git tree to store along side as meta data.
// The meta size data is stored via the trees hash plus extension under the objects
// directory.
func (o *Objects) SaveDirEntries(reader cas.DirEntryReader) (hash.Hash, error) {
	hash, buf, sData, err := EncodeDirEntries(o.Hasher, reader)
	if err != nil {
		return nil, err
	}
	// Write the directory entries and the meta data
	if err := o.WriteMeta(hash, treeMetaTag, bytes.NewBuffer(sData), uncompressed); err != nil {
		return nil, err
	}
	if err := o.Write(hash, bytes.NewBuffer(buf), compressed); err != nil {
		return nil, err
	}

	return hash, nil
}

// SaveDeepTree save the input deep tree in the store
func (o *Objects) SaveDeepTree(reader cas.DirEntryReader) (hash.Hash, error) {
	hash, content, err := EncodeDeepTree(o.Hasher, reader)
	if err != nil {
		return nil, err
	}
	// Commit to store
	if err := o.Write(hash, bytes.NewBuffer(content), compressed); err != nil {
		return nil, err
	}

	return hash, nil
}

// GetDirEntries read the git tree and size meta data and create DirEntries
func (o *Objects) GetDirEntries(h hash.Hash) (cas.DirEntryReadCloser, error) {
	// Just decoding full into memory and then provider a reader
	// Could change to real stream if needed
	raw, err := o.ReadAll(h, compressed)
	if err != nil {
		return nil, err
	}
	rawMeta, err := o.ReadMeta(h, treeMetaTag, uncompressed)
	if err != nil {
		return nil, err
	}
	t, buf, err := decodeObject(raw)
	if err != nil {
		return nil, err
	}
	if t != plumbing.TreeObject.String() {
		return nil, fmt.Errorf("Wrong object type <%s> expected <%s>",
			t, plumbing.TreeObject)
	}

	sizeLen := len(rawMeta)
	sizeOffset := 0

	var entries []cas.DirEntry
	var i int
	for i = 0; ; i++ {
		p := bytes.IndexByte(buf, ' ')
		if p < 0 {
			break
		}
		entMode, err := strconv.ParseUint(string(buf[:p]), 8, 32)
		if err != nil {
			return nil, err
		}
		mode, extType, err := GetTypeFromMode(Mode(entMode))
		if err != nil {
			return nil, err
		}
		buf = buf[p+1:]
		if p = bytes.IndexByte(buf, 0); p < 0 {
			return nil, ErrObjectParse
		}

		name := string(buf[0:p])

		hash := o.Hasher.ZeroHash()
		if _, err := hash.Read(bytes.NewReader(buf[p+1:])); err != nil {
			return nil, err
		}

		buf = buf[p+21:]
		var size int64
		if sizeOffset < sizeLen {
			size = int64(binary.LittleEndian.Uint64(rawMeta[sizeOffset : sizeOffset+8]))
			sizeOffset += 8
		}

		entries = append(entries,
			cas.DirEntry{
				Hash:    hash,
				Size:    size,
				Mode:    mode,
				Name:    name,
				ExtType: extType,
			})
	}
	return cas.NewDirInMemory(entries), nil
}

// GetDeepTree open the deep tree and return reader to the user
// User is responsible for closing the reader when done
func (o *Objects) GetDeepTree(h hash.Hash) (cas.DirEntryReadCloser, error) {
	f, err := o.Open(h, compressed)
	if err != nil {
		return nil, err
	}
	_, err = readTreeHdr(f.Reader(), true)
	if err != nil {
		f.Close()
		return nil, err
	}
	// Caller will close the files
	return NewDeepTreeReader(f, o.Hasher), nil

}

// SaveDirList create hash of the names in a directory
// Currently we don't persist this information and instead are only
// interested in the hash value
func (o *Objects) SaveDirList(r cas.DirListReader) (hash.Hash, error) {
	return EncodeDirList(o.Hasher, r)
}

// GetModeFromType convert casDirOjbectType to git type
func GetModeFromType(e *cas.DirEntry) (Mode, error) {

	if e.Mode.IsDir() {
		switch e.ExtType {
		case cas.DirObjectExtTypeSubModule:
			return ModeSubModule, nil
		case cas.DirObjectExtTypeDeepTree:
			return CASModeDeepTree, nil
		}
		return ModeDir, nil
	}

	if e.Mode.IsRegular() {
		if (e.Mode.Perm() & 0111) != 0 {
			return ModeExecutable, nil
		}
		return ModeRegular, nil
	}

	if e.Mode&fs.ModeSymlink != 0 {
		return ModeSymLink, nil
	}
	if e.Mode&fs.ModeNamedPipe != 0 {
		return ModeFifo, nil
	}
	if e.Mode&fs.ModeSocket != 0 {
		return CASModeSocket, nil
	}

	return 0, ErrUnknownType
}

// GetTypeFromMode convert git mode to casDirOjbectType
func GetTypeFromMode(mode Mode) (cas.FileMode, cas.DirObjectExtType, error) {
	var osMode cas.FileMode
	extType := cas.DirObjectExtTypeNone
	switch mode {
	case ModeRegular:
		osMode = cas.FileModeFile()
	case ModeExecutable:
		osMode = cas.FileModeExecutable()
	case ModeDir:
		osMode = cas.FileModeDir()
	case ModeSubModule:
		osMode = cas.FileModeDir()
		extType = cas.DirObjectExtTypeSubModule
	case CASModeDeepTree:
		osMode = cas.FileModeDir()
		extType = cas.DirObjectExtTypeDeepTree
	case ModeSymLink:
		osMode = cas.FileModeSymLink()
	case ModeFifo:
		osMode = cas.FileModeFifo()
	case CASModeSocket:
		osMode = cas.FileModeSocket()
	default:
		return 0, extType, ErrUnknownType
	}

	return osMode, extType, nil
}

func parseHeader(content []byte) (string, int64, []byte, error) {
	p := bytes.IndexByte(content, ' ')
	if p < 0 {
		return "", 0, nil, ErrObjectParse
	}
	t := string(content[0:p])
	content = content[p+1:]
	p = bytes.IndexByte(content, 0)
	if p < 0 {
		return "", 0, nil, ErrObjectParse
	}
	s, err := strconv.ParseInt(string(content[0:p]), 10, 64)
	if err != nil {
		return "", 0, nil, ErrObjectParse
	}
	return t, s, content[p+1:], nil
}
func decodeObject(content []byte) (string, []byte, error) {
	t, s, content, err := parseHeader(content)
	if err != nil {
		fmt.Printf("Decode parse header problem\n")
		return "", nil, err
	}
	if s != int64(len(content)) {
		fmt.Printf("Size mismatch %d vs %d\n", s, len(content))
		return "", nil, ErrObjectParse
	}

	return t, content, nil
}

func (b *Objects) Query(h hash.Hash) (*cas.QueryInfo, error) {
	f, err := b.Open(h, compressed)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	delim := byte(0)

	reader := bufio.NewReader(f)
	var content []byte
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return nil, err
		}
		content = append(content, b)
		if b == delim {
			break
		}

	}
	objType, size, _, err := parseHeader(content)
	if err != nil {
		return nil, err
	}
	switch objType {
	case "tree":
		return &cas.QueryInfo{Type: cas.StoreItemTypeDirEntries, Size: size}, nil
	case "deeptree":
		return &cas.QueryInfo{Type: cas.StoreItemTypeDeepTree, Size: size}, nil

	}
	return nil, fmt.Errorf("unknown content <%s>", objType)
}
func (o *Objects) WriteObject(h hash.Hash, reader io.Reader) error {
	return o.Write(h, reader, compressed)
}
