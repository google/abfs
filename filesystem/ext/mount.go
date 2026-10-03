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

package ext

import (
	"log"
	"time"

	"github.com/google/abfs/filesystem"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
)

func NewMount() filesystem.Mount {
	return filesystem.NewCasfsMount(&extMount{run: true})
}

type extMount struct {
	run bool
}

var _ filesystem.OsMounter = (*extMount)(nil)

func (m *extMount) DoMount(mountPoint string, _ *filesystem.MountInfo, _ *entities.FSConfig, _ entities.CASStore) (bindcas.CASFS, error) {
	log.Printf("Attaching to existing external filesystem at %s", mountPoint)
	return bindcas.NewExternalCASFS(mountPoint), nil
}

func (m *extMount) DoServe() error {
	for m.run {
		time.Sleep(1 * time.Second)
	}
	return nil
}

func (m *extMount) DoUnmount(mountPoint string) error {
	m.run = false
	log.Printf("Unmount ignored — leaving %s mounted", mountPoint)
	return nil
}
