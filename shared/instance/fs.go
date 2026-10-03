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

package instance

import (
	"errors"
	"fmt"
)

type CASFSType string

const (
	CASFSTypeKmod CASFSType = "kmod"
	CASFSTypeExt  CASFSType = "ext"
)

func (t *CASFSType) Set(value string) error {
	switch CASFSType(value) {
	case CASFSTypeKmod:
		*t = CASFSTypeKmod
	case CASFSTypeExt:
		*t = CASFSTypeExt
	default:
		return errors.New("invalid filesystem type")
	}
	return nil
}
func (t *CASFSType) String() string {
	return string(*t)
}

func (t *CASFSType) Type() string {
	return "CASFSType"
}

func CASFSTypes() string {
	return fmt.Sprintf("%s,%s", CASFSTypeKmod, CASFSTypeExt)
}

func (CASFSType) EnumValues() [][2]string {
	return [][2]string{
		{string(CASFSTypeKmod), "Kernel module filesystem"},
		{string(CASFSTypeExt), "External filesystem"},
	}
}
