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
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/utils/ioutil"
	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/hash"
)

// hash a directory list
func EncodeDirList(hasher hash.Hasher, r cas.DirListReader) (hash.Hash, error) {
	hasherFunc := hasher.New()
	for {
		n, err := r.Read()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}

		if _, err := fmt.Fprintf(hasherFunc, "%s", n.Name); err != nil {
			return nil, err
		}
		if _, err := hasherFunc.Write([]byte{0x00}); err != nil {
			return nil, err
		}
	}

	out := hasherFunc.Sum(nil)
	return out, nil
}

// Encode the directory entries
func EncodeDirEntries(hasher hash.Hasher, reader cas.DirEntryReader) (hash hash.Hash, data, meta []byte, err error) {
	obj := &plumbing.MemoryObject{}
	obj.SetType(plumbing.TreeObject)

	w, err := obj.Writer()
	if err != nil {
		return nil, nil, nil, err
	}

	defer ioutil.CheckClose(w, &err)

	sizes := make([]int64, 0)

	for {
		n, err := reader.Read()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, nil, nil, err
		}
		mode, err := GetModeFromType(n)
		if err != nil {
			return nil, nil, nil, err
		}
		if _, err = fmt.Fprintf(w, "%o %s", mode, n.Name); err != nil {
			return nil, nil, nil, err
		}
		if _, err = w.Write([]byte{0x00}); err != nil {
			return nil, nil, nil, err
		}

		if err != nil {
			return nil, nil, nil, err
		}
		if _, err := n.Hash.Write(w); err != nil {
			return nil, nil, nil, err
		}
		sizes = append(sizes, int64(n.Size))
	}
	hash = hasher.ZeroHash()
	oHash := obj.Hash()
	if _, err := hash.Read(bytes.NewReader(oHash[:])); err != nil {
		return nil, nil, nil, err
	}

	// Write the tree with git header
	r, err := obj.Reader()
	if err != nil {
		return nil, nil, nil, err
	}
	defer r.Close()
	buf := bytes.NewBuffer(nil)
	fmt.Fprintf(buf, "%s %d\x00", obj.Type().Bytes(), obj.Size())
	if _, err := io.Copy(buf, r); err != nil {
		return nil, nil, nil, err
	}

	// Create size meta data
	sData := make([]byte, 8*len(sizes))

	for i, s := range sizes {
		binary.LittleEndian.PutUint64(sData[i*8:], uint64(s))
	}
	return hash, buf.Bytes(), sData, nil
}

// Encode deep tree
func EncodeDeepTree(hasher hash.Hasher, reader cas.DirEntryReader) (hash.Hash, []byte, error) {
	buf := bytes.NewBuffer(nil)
	buf.Write(make([]byte, MaxHeaderSize))

	varint := make([]byte, binary.MaxVarintLen64)

	for {
		n, err := reader.Read()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, nil, err
		}
		_, _ = n.Hash.Write(buf)
		vn := binary.PutUvarint(varint, uint64(n.Size))
		buf.Write(varint[:vn])
		m, err := GetModeFromType(n)
		if err != nil {
			return nil, nil, err
		}
		vn = binary.PutUvarint(varint, uint64(m))
		buf.Write(varint[:vn])
		buf.WriteString(n.Name)
		buf.WriteByte('\x00')

	}

	content, err := putHeader(deepTreeName, buf.Bytes())
	if err != nil {
		return nil, nil, err
	}
	sHasher := hasher.New()
	hash := sHasher.Sum(content)
	return hash, content, nil
}
