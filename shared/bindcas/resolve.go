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

package bindcas

import (
	"github.com/google/abfs/cas/cas"

	"github.com/google/abfs/shared/entities"
)

type ABFSCASStore interface {
	CAS() cas.Store
}

func Resolve(s ABFSCASStore, hash entities.Hash, p string) (*cas.DirEntry, error) {
	return cas.Resolve(s.CAS(), CASHash(&hash), p)
}
func ResolveToHash(s ABFSCASStore, hash entities.Hash, p string) (entities.Hash, error) {
	var out entities.Hash
	de, err := Resolve(s, hash, p)
	if err != nil {
		return out, err
	}
	out = ABFSHash(de.Hash)
	return out, nil
}
