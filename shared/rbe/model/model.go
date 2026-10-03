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

package model

import (
	"fmt"
)

// NodeType represents the type of entry in the RBE ecosystem.
type NodeType int

const (
	NodeTypeFile NodeType = iota
	NodeTypeSymlink
	NodeTypeDirectory
	NodeTypeDeepTree
)

func (t NodeType) String() string {
	switch t {
	case NodeTypeFile:
		return "file"
	case NodeTypeSymlink:
		return "symlink"
	case NodeTypeDirectory:
		return "directory"
	case NodeTypeDeepTree:
		return "deeptree"
	default:
		return fmt.Sprintf("unknown(%d)", t)
	}
}

// Digest holds the key to querying RBE.
type Digest struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Node holds the RBE-specific metadata for a content-addressed object.
// This is used in the identity side-channel to map ABFS SHA-1 hashes to RBE digests.
type Node struct {
	Digest Digest
	Type   NodeType
}
