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
	"io"
	"strconv"
)

func readByte(rw io.Reader) (byte, error) {
	cur := make([]byte, 1)
	n, err := rw.Read(cur)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, ErrObjectParse
	}
	return cur[0], nil

}
func readVarint(rw io.Reader) (uint64, error) {
	var x uint64
	var s uint
	const maxVarintLen64 int = 10
	n := 0
	for {
		if n >= 10 {
			return 0, ErrObjectParse
		}
		b, err := readByte(rw)
		if err != nil {
			return 0, err
		}
		if b < 0x80 {
			if n == maxVarintLen64-1 && b > 1 {
				return 0, ErrObjectParse
			}
			return x | uint64(b)<<s, nil
		}
		x |= uint64(b&0x7f) << s
		s += 7
		n++
	}
}
func read(rw io.Reader, sz int) ([]byte, error) {
	out := make([]byte, 0, sz)
	for i := 0; i < sz; i++ {
		val, err := readByte(rw)
		if err != nil {
			return nil, err
		}
		out = append(out, val)
	}
	return out, nil
}
func readDelim(rw io.Reader, delim *byte, incDelim bool) ([]byte, error) {
	out := make([]byte, 0, 64)
	var err error
	var val byte
	for {
		val, err = readByte(rw)
		if err != nil {
			break
		}
		if delim != nil && (val == *delim) {
			if incDelim {
				out = append(out, val)
			}
			return out, nil

		}
		out = append(out, val)
	}
	if delim != nil {
		if *delim == 0 && err == io.EOF {
			return out, nil
		}
		return nil, ErrNoDelim
	}
	return out, nil
}

func parseTreeType(val []byte) (deeptree bool, err error) {
	sval := string(val)

	switch sval {
	case "tree":
		return false, nil
	case "deeptree":
		return true, nil

	}
	return false, ErrObjectParse
}
func readTreeHdr(rw io.Reader, deepTree bool) (uint64, error) {

	var raw []byte
	var err error
	var delim byte = ' '
	raw, err = readDelim(rw, &delim, false)
	if err != nil {
		return 0, err
	}

	isDeepTree, err := parseTreeType(raw)
	if err != nil {
		return 0, err
	}
	if isDeepTree != deepTree {
		return 0, ErrObjectParse
	}

	delim = 0
	raw, err = readDelim(rw, &delim, false)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(string(raw), 10, 64)
}
