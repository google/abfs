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

package main

import "github.com/pkg/xattr"

func GetXAttr(path, name string) (string, error) {
	data, err := xattr.LGet(path, "casfs."+name)
	return string(data), err
}
func SetXAttr(path, name string, data string) error {
	return xattr.LSet(path, "casfs."+name, []byte(data))
}

const (
	HashAttrName              = "hash"
	HashSizeAttrName          = "hash_size"
	DirListHashAttrName       = "dir_list_hash"
	DeepTreeAttrName          = "deeptree"
	CtrlAttrName              = "ctrl"
	DebugAttrName             = "debug"
	DirtyAttrName             = "dirty"
	DiagsAttrName             = "diags"
	LoadedAttrName            = "loaded"
	UpdateApproachAttrName    = "update_approach"
	UpdateApproachRawAttrName = "update_approach_raw"
	ExcludeAttrName           = "exclude"
)
