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

package kmod

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"time"

	"github.com/google/abfs/filesystem"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/instance"
)

func NewMount() filesystem.Mount {
	return filesystem.NewCasfsMount(&kmodMount{run: true})
}

type kmodMount struct {
	run bool
}

var _ filesystem.OsMounter = (*kmodMount)(nil)

func (m *kmodMount) DoMount(path string, info *filesystem.MountInfo, _ *entities.FSConfig, _ entities.CASStore) (bindcas.CASFS, error) {

	args := []string{path, "--cache-dir", info.CacheDir}
	if info.LocalOnly {
		args = append(args, "--local")
	}
	if info.FetchTimeout != instance.DefaultFetchTimeout {
		args = append(args, "--fetch-timeout", fmt.Sprintf("%d", info.FetchTimeout.Milliseconds()))
	}
	if info.PartialClone {
		args = append(args, "--partial-clone")
	}
	bin, err := CasfsMounterPath()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("mount failed: %w", err)
	}
	fmt.Println("CASFS kmod mounted")
	return bindcas.NewExternalCASFS(path), nil

}
func (m *kmodMount) DoServe() error {
	for m.run {
		time.Sleep(1 * time.Second)
	}
	return nil

}
func (m *kmodMount) DoUnmount(path string) error {
	m.run = false
	bin, err := CasfsMounterPath()
	if err != nil {
		log.Printf("failed to find casfs-mounter binary: %v", err)
		return err
	}
	return exec.Command(bin, "-u", path).Run()
}

// CasfsMounterPath finds the path of the casfs-mounter binary in case
// it isn't in the current $PATH
func CasfsMounterPath() (string, error) {
	bin := "casfs-mounter"
	execPath, firstErr := exec.LookPath(bin)
	if firstErr == nil { // no error
		return execPath, nil
	}
	// try the folder that this binary executed from
	abfsPath, err := os.Executable()
	if err != nil {
		return "", firstErr
	}
	binDir := path.Dir(abfsPath)
	tryPath := path.Join(binDir, bin)
	if _, err := os.Stat(tryPath); err != nil {
		return "", firstErr
	}
	return tryPath, nil
}
