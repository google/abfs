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

import "github.com/google/abfs/cas/hash"

// Provider a full read and write interface to a remote provider.  The providers
// know how to populate and retrieve content from the local store or remote
// store based on the Hash value.
type Provider interface {
	Open() error
	Close() error

	ProviderReader
	ProviderWriter
}

// ProviderReader provides the interface to communicate with a remote provider.
// This interface is used to bring content locally from a remote end point.
// After a successful call to read data the local cache can be used to
// access the data.
type ProviderReader interface {
	FetchFile(hash.Hash) error
	FetchSymLink(hash.Hash) error
	FetchDirList(hash.Hash) error
	FetchDirEntries(hash.Hash) error
	FetchDeepTree(hash.Hash) error
}

// ProviderWriter provides the interface to communicate with a remote provider.
// This interface is used to push local content to the remote end point.
type ProviderWriter interface {
	NewFile(hash.Hash) error
	NewSymLink(hash.Hash) error
	NewDirList(hash.Hash) error
	NewDirEntries(hash.Hash) error
	NewDeepTree(hash.Hash) error
}
