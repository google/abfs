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

package args

import (
	"fmt"
	"strings"
)

type FSType string
type FSTypes []FSType

const (
	FSTypeRegular    FSType = "file"
	FSTypeExecutable        = "executable"
	FSTypeDirectory         = "dir"
	FSTypeDirty             = "dirty"
)

func (f *FSTypes) Set(val string) error {
	switch FSType(val) {
	case FSTypeRegular:
		*f = append(*f, FSTypeRegular)
	case FSTypeExecutable:
		*f = append(*f, FSTypeExecutable)
	case FSTypeDirectory:
		*f = append(*f, FSTypeDirectory)
	case FSTypeDirty:
		*f = append(*f, FSTypeDirty)
	default:
		return fmt.Errorf("invalid type %s", val)
	}
	return nil
}
func (f *FSTypes) String() string {
	out := make([]string, len(*f))

	for i, t := range *f {
		out[i] = string(t)
	}
	return strings.Join(out, ",")
}
func (*FSTypes) Type() string {
	return "FSTypes"
}

func FSAllTypes() string {
	return fmt.Sprintf("%s,%s,%s,%s", FSTypeRegular, FSTypeExecutable, FSTypeDirectory, FSTypeDirty)
}
