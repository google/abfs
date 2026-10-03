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

package cas

import (
	"fmt"

	"github.com/google/abfs/cas/hash"
)

type test_provider struct {
	remote *test_store // represents the remote store
	local  *test_store // Test store to write to
}

var _ Provider = (*test_provider)(nil)

var notFoundErr = fmt.Errorf("not found")

func (t *test_provider) Open() error {
	return nil
}
func (t *test_provider) Close() error {
	return nil
}
func (t *test_provider) FetchFile(h hash.Hash) error {
	if val, ok := t.remote.files[h.String()]; ok {
		t.local.files[h.String()] = val
		return nil
	}
	return notFoundErr
}

func (t *test_provider) FetchSymLink(h hash.Hash) error {
	if val, ok := t.remote.symLinks[h.String()]; ok {
		t.local.symLinks[h.String()] = val
		return nil
	}
	return notFoundErr
}
func (t *test_provider) FetchDirList(h hash.Hash) error {
	if val, ok := t.remote.dirLists[h.String()]; ok {
		t.local.dirLists[h.String()] = val
		return nil
	}
	return notFoundErr
}

func (t *test_provider) FetchDirEntries(h hash.Hash) error {
	if val, ok := t.remote.dirEntries[h.String()]; ok {
		t.local.dirEntries[h.String()] = val
		return nil
	}
	return notFoundErr
}
func (t *test_provider) FetchDeepTree(h hash.Hash) error {
	if val, ok := t.remote.deepTrees[h.String()]; ok {
		t.local.deepTrees[h.String()] = val
		return nil
	}
	return notFoundErr
}
func (t *test_provider) NewFile(h hash.Hash) error {
	if val, ok := t.local.files[h.String()]; ok {
		t.remote.files[h.String()] = val
		return nil
	}
	return notFoundErr

}
func (t *test_provider) NewSymLink(h hash.Hash) error {
	if val, ok := t.local.symLinks[h.String()]; ok {
		t.remote.symLinks[h.String()] = val
		return nil
	}
	return notFoundErr
}
func (t *test_provider) NewDirList(h hash.Hash) error {
	if val, ok := t.local.dirLists[h.String()]; ok {
		t.remote.dirLists[h.String()] = val
		return nil
	}
	return notFoundErr
}

func (t *test_provider) NewDirEntries(h hash.Hash) error {
	if val, ok := t.local.dirEntries[h.String()]; ok {
		t.remote.dirEntries[h.String()] = val
		return nil
	}
	return notFoundErr
}
func (t *test_provider) NewDeepTree(h hash.Hash) error {
	if val, ok := t.local.deepTrees[h.String()]; ok {
		t.remote.deepTrees[h.String()] = val
		return nil
	}
	return notFoundErr

}
