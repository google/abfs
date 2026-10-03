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

package remote

import (
	"fmt"
	"hash"
	"io"
	"log"
	"sync"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
)

type Validator struct {
	hash         Hash
	encoding     string
	linksExtract bool
	err          error
	hasher       hash.Hash
	reader       io.ReadCloser
	pw           *io.PipeWriter
	header       []byte // if nil, header has been read
	objSize      int64
	objType      string
	links        []Hash
	readSize     int64
	content      []byte
	wg           sync.WaitGroup
}

type ValidatorOpts interface{}
type validateHashOpt struct {
	value bool
}

func ValidateHashOpt(value bool) ValidatorOpts {
	return validateHashOpt{value: value}
}

type extractLinksOpt struct {
	value bool
}

func ExtractLinksOpt(value bool) ValidatorOpts {
	return extractLinksOpt{value: value}
}

func NewValidator(hash Hash, encoding string, opts ...ValidatorOpts) (*Validator, error) {

	pr, pw := io.Pipe()
	v := &Validator{
		hash:     hash,
		encoding: encoding,
		pw:       pw,
		header:   make([]byte, 0, objects.MaxHeaderSize),
	}

	for _, opt := range opts {
		switch opt := opt.(type) {
		case validateHashOpt:
			if opt.value {
				v.hasher = objects.NewHasher()
			}
		case extractLinksOpt:
			v.linksExtract = opt.value
		default:
			return nil, fmt.Errorf("unknown option type %T", opt)
		}
	}

	reader, err := DecodeToRaw(NewContentReader(pr, encoding))
	if err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return nil, err
	}

	v.reader = reader

	v.wg.Add(1)
	go func() {
		defer func() { _ = v.reader.Close() }()
		defer v.wg.Done()
		defer func() {
			if err := recover(); err != nil {
				v.setErr(fmt.Errorf("panic: %v", err))
			}
		}()

		buf := make([]byte, 1024)
		for {
			n, err := v.reader.Read(buf)
			if v.hasher != nil {
				// Hasher never returns an error
				v.hasher.Write(buf[:n])
			}
			i := 0
			if v.header != nil { // Header is not read yet
				for i = 0; i < n; i++ {
					v.header = append(v.header, buf[i])

					if buf[i] == 0 { // Header is NULL terminated
						t, n, err := objects.ParseHeader(v.header[:len(v.header)-1])
						if err != nil {
							v.setErr(err)
							v.header = nil
						}
						v.objSize = n
						v.objType = t
						if t != string(entities.ObjectTypeBlob) && v.linksExtract {
							v.content = v.header
						}
						v.header = nil // Header is fully read, set it to nil to indicate it
						i++
						break
					}
					if len(v.header) > objects.MaxHeaderSize { // Header NULL termination is not found in MaxHeaderSize first bytes
						v.setErr(fmt.Errorf("object header not found"))
						v.header = nil
						break
					}
				}
			}
			if err != nil {
				if err == io.EOF {
					break
				}
				v.setErr(err)
				break
			}
			if v.content != nil {
				v.content = append(v.content, buf[i:n]...)
			}
			v.readSize += int64(n - i)
		}
		if v.header != nil { // Header is not nil, it means it is not fully read
			v.setErr(fmt.Errorf("object header not found"))
		}
		if v.readSize != v.objSize {
			v.setErr(fmt.Errorf("expected object size to be size %d, but got %d", v.objSize, v.readSize))
		}

		if v.content != nil {
			if err := v.extractLinks(); err != nil {
				v.setErr(err)
			}
		}
		if v.hasher != nil {
			got := v.Hash()
			if got != v.hash {
				v.setErr(fmt.Errorf("expected hash %q, but got %q", v.hash, got))
			}
		}
	}()
	return v, nil
}

func (v *Validator) setErr(err error) {
	if v.err == nil && err != nil {
		v.err = err
	}
}

func (v *Validator) Write(p []byte) (int, error) {
	// TODO probably more efficient to use a buffer
	return v.pw.Write(p)
}

func (v *Validator) Close() error {
	_ = v.pw.Close()
	v.wg.Wait()
	return v.err
}

func (v *Validator) Size() int64 {
	return v.objSize
}

func (v *Validator) Type() string {
	return v.objType
}

func (v *Validator) Hash() Hash {
	var hash Hash
	if v.hasher != nil {
		copy(hash[:], v.hasher.Sum(nil))
	}
	return hash
}

func (v *Validator) Links() []Hash {
	return v.links
}

func (v *Validator) extractLinks() error {
	_, obj, err := objects.Factory.Decode(v.hash, v.content)
	if err != nil {
		return fmt.Errorf("failed to extract links from the provided object %q, err: %v", v.hash, err)
	}

	objLinks := obj.Links()
	links := make([]Hash, 0, len(objLinks))

	for _, link := range objLinks {
		if link.IsZero() {
			log.Printf("WARNING: object %q had a link to the ZeroHash and this is likely a bug. link: %v", v.hash, link)
			// we don't want to tell clients that they need to upload the ZeroHash because it won't ever succeed
			continue
		}
		links = append(links, link)
	}

	v.links = links

	return nil
}
