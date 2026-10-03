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

package providers

import (
	"time"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/cas/providers/cacheman"
)

type ProviderType int

const (
	ProviderTypeCacheman ProviderType = iota
)

func (o ProviderType) String() string {
	switch o {
	case ProviderTypeCacheman:
		return "cacheman"
	}
	return ""
}

// CreateProvider create the provider based on type
func CreateProvider(t ProviderType, endpoint string, timeout time.Duration) cas.Provider {

	switch t {
	case ProviderTypeCacheman:
		return cacheman.NewProvider(endpoint, timeout)
	}
	return nil
}
