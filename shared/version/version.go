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

package version

import (
	"strconv"
	"time"
)

// this is magically filled in through the Makefile
var version = "0.0.0"

// this is magically filled in through the Makefile
// This is the client side expiration time in unix epoch seconds
var expiration = ""

// HasExpiration returns whether the binary has an expiration time
func HasExpiration() bool {
	return expiration != ""
}

// Expiration returns the client side expiration time, if applicable
func Expiration() (time.Time, error) {
	unixSec, err := strconv.ParseInt(expiration, 10, 64)
	if err != nil {
		return time.Time{}, err
	}

	return time.Unix(unixSec, 0), nil
}

// Version returns the application version if the running binary was built with the Makefile
func Version() string {
	return version
}

const (
	// ABFSCachemanImplementedVersion is the virtual interface version ABFS client implements for Cacheman.
	ABFSCachemanImplementedVersion = "1.0.0"

	// ABFSCachemanRequiredVersion is the minimum virtual interface version ABFS client requires from Cacheman.
	ABFSCachemanRequiredVersion = "1.0.0"

	// ABFSCASFSImplementedVersion is the virtual interface version ABFS client implements for CASFS.
	ABFSCASFSImplementedVersion = "1.5.0"

	// ABFSCASFSRequiredVersion is the minimum virtual interface version ABFS client requires from CASFS.
	ABFSCASFSRequiredVersion = "1.5.0"

	// CachemanABFSImplementedVersion is the virtual interface version Cacheman daemon implements for ABFS.
	CachemanABFSImplementedVersion = "1.0.0"

	// CachemanABFSRequiredVersion is the minimum virtual interface version Cacheman daemon requires from ABFS client.
	CachemanABFSRequiredVersion = "1.0.0"
)
