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

package oms

import (
	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/cas/oms/git"
	"github.com/google/abfs/cas/hash"
)

type ObjectModelType int

const (
	ObjectModelTypeGIT ObjectModelType = iota
)

func (o ObjectModelType) String() string {
	switch o {
	case ObjectModelTypeGIT:
		return "GIT"
	}
	return ""
}

// CreateObjectModel create the object model based on type
func CreateObjectModel(t ObjectModelType, hasher hash.Hasher, root, scratch string, exclusiveScratch bool) cas.Store {

	switch t {
	case ObjectModelTypeGIT:
		return git.NewCASStore(root, scratch, hasher, exclusiveScratch)
	}
	return nil
}
