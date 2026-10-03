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

package factory

import (
	"fmt"

	"github.com/google/abfs/filesystem"
	"github.com/google/abfs/filesystem/ext"
	"github.com/google/abfs/filesystem/kmod"
	"github.com/google/abfs/shared/instance"
)

func NewMount(t instance.CASFSType) filesystem.Mount {
	switch t {
	case instance.CASFSTypeKmod:
		return kmod.NewMount()
	case instance.CASFSTypeExt:
		return ext.NewMount()
	default:
		panic(fmt.Sprintf("unsupported fs type %s", t))
	}
}
