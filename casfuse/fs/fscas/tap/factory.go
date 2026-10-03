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

package tap

import (
	"bufio"
	"bytes"
	"sync"

	"github.com/google/abfs/cas/hash"
)

var _ Tap = (*tap)(nil)

func NewTap(hasher hash.Hasher) Tap {
	return &tap{hasher: hasher}
}

func newSession(sid int, hasher hash.Hasher) *Session {
	buf := bytes.NewBuffer(nil)

	s := &Session{
		lock:   new(sync.Mutex),
		wg:     new(sync.WaitGroup),
		buf:    buf,
		w:      bufio.NewWriter(buf),
		sid:    sid,
		hasher: hasher,
	}
	// Writes to bytes.Buffer won't fail
	_ = s.w.WriteByte(TapSeqVersion)
	return s
}
