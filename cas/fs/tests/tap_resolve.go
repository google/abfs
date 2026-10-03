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

package tests

import (
	"bytes"
	"fmt"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/google/abfs/cas/fs"
)

func (t *tapTest) RunNonTapCmdAndResolve(workspace string, cmd *exec.Cmd) error {
	stdoutB := new(bytes.Buffer)
	stderrB := new(bytes.Buffer)
	cmd.Dir = workspace
	cmd.Stdout = stdoutB
	cmd.Stderr = stderrB
	if err := cmd.Run(); err != nil {
		return err
	}
	// Hash up the workspace so nothing is dirty
	if _, err := t.fs.Hash(workspace); err != nil {
		return err
	}
	return nil

}
func (t *tapTest) RunResolvedCmd(workspace string, cmdArgs []string) (*fs.TapCmdResolvedResult, error) {
	// Need to run via casfsapi tap to get the correct pgid
	// All tests here are resolve tests
	tPath := path.Join(strings.TrimPrefix(workspace, t.fs.MountPath()), ".ctrl", "tap")
	cmd := exec.Command(t.cli, "--mount-point", t.fs.MountPath(), "tap", "--tapPath", tPath, "--")
	cmd.Args = append(cmd.Args, cmdArgs...)
	stdoutB := new(bytes.Buffer)
	stderrB := new(bytes.Buffer)
	cmd.Dir = workspace
	cmd.Stdout = stdoutB
	cmd.Stderr = stderrB
	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("tap error: %s (%s)", err, cmd.Stderr)
	}
	result := fs.NewTapCmdResolvedResult()
	err = result.UnmarshalJSONWithHasher(stdoutB.Bytes(), t.hasher)
	if err != nil {
		return nil, fmt.Errorf("tap unmarshal error %s", err)
	}
	return result, nil
}
func tapName(mntRoot, workspace, name string) string {
	return filepath.Join("/", strings.TrimPrefix(workspace, mntRoot), name)
}
func (t *tapTest) FileReadResolved(mntRoot, workspace string) error {
	err := t.CtrlTapTest(workspace)
	if err != nil {
		return err
	}

	name := "test_file"
	sCmd := fmt.Sprintf("echo test_data_for_file > %s", name)
	c := exec.Command("/bin/sh", "-c", sCmd)
	if err := t.RunNonTapCmdAndResolve(workspace, c); err != nil {
		return err
	}
	res, err := t.RunResolvedCmd(workspace, []string{"cat", name})
	if err != nil {
		return err
	}
	if len(res.Deletes) != 0 {
		return fmt.Errorf("unexpected delete count %d", len(res.Deletes))
	}
	if len(res.Writes) != 0 {
		return fmt.Errorf("unexpected write count %d", len(res.Writes))
	}
	if len(res.Reads) != 1 {
		return fmt.Errorf("unexpected read count %d", len(res.Reads))
	}
	for _, h := range res.Reads {
		if h.Hash.IsEmptyFile() {
			return fmt.Errorf("unexpected empty file hash")
		}
	}
	return nil

}

func (t *tapTest) FileCreateDeleteMkdirResolved(mntRoot, workspace string) error {
	err := t.CtrlTapTest(workspace)
	if err != nil {
		return err
	}

	name := "test_file"

	cmd := []string{"/bin/sh", "-c", fmt.Sprintf("echo file > %s; rm %s; mkdir %s", name, name, name)}
	res, err := t.RunResolvedCmd(workspace, cmd)
	if err != nil {
		return err
	}

	// Expect file to be in delete set
	if len(res.Deletes) != 1 {
		return fmt.Errorf("unexpected delete count %d", len(res.Deletes))
	}
	if len(res.Writes) != 1 {
		return fmt.Errorf("unexpected write count %d", len(res.Writes))
	}
	if len(res.Reads) != 0 {
		return fmt.Errorf("unexpected read count %d", len(res.Reads))
	}
	resolveName := tapName(mntRoot, workspace, name)

	for k, _ := range res.Deletes {
		if k != resolveName {
			return fmt.Errorf("Expected delete file %s got %s", resolveName, k)
		}
	}
	for k, hs := range res.Writes {
		if k != resolveName {
			return fmt.Errorf("Expected write file %s got %s", resolveName, k)
		}
		if hs.Size != 0 {
			return fmt.Errorf("Expected 0 size got %d", hs.Size)
		}

		if !hs.Hash.IsEmptyDir() {
			return fmt.Errorf("Expected empty hash got %s", hs.Hash)
		}
		if !hs.Mode.IsDir() {
			return fmt.Errorf("Expected directory got mode %o", hs.Mode)
		}
	}

	return nil
}

func (t *tapTest) FileCreateRenameResolved(mntRoot, workspace string) error {
	err := t.CtrlTapTest(workspace)
	if err != nil {
		return err
	}

	name := "test_file"
	name2 := "end_file"

	cmd := []string{"/bin/sh", "-c", fmt.Sprintf("echo file > %s; mv %s %s", name, name, name2)}

	res, err := t.RunResolvedCmd(workspace, cmd)
	if err != nil {
		return err
	}

	// Expect name to be in delete set
	if len(res.Deletes) != 1 {
		return fmt.Errorf("unexpected delete count %d", len(res.Deletes))
	}
	// Expect name2 to be in write set
	if len(res.Writes) != 1 {
		return fmt.Errorf("unexpected write count %d", len(res.Writes))
	}
	if len(res.Reads) != 0 {
		return fmt.Errorf("unexpected read count %d", len(res.Reads))
	}
	resolveName := tapName(mntRoot, workspace, name)
	resolveName2 := tapName(mntRoot, workspace, name2)

	for k, _ := range res.Deletes {
		if k != resolveName {
			return fmt.Errorf("Expected delete file %s got %s", resolveName, k)
		}
	}
	for k, hs := range res.Writes {
		if k != resolveName2 {
			return fmt.Errorf("Expected write file %s got %s", resolveName2, k)
		}
		if hs.Hash.IsEmptyFile() {
			return fmt.Errorf("Expected non empty-hash got empty hash")
		}
		if !hs.Mode.IsRegular() {
			return fmt.Errorf("Expected file got mode %o", hs.Mode)
		}
	}

	return nil
}

func (t *tapTest) FileRenameDirResolved(mntRoot, workspace string) error {
	// Create a files and subdirectories and then rename the top directory
	err := t.CtrlTapTest(workspace)
	if err != nil {
		return err
	}

	file := "test_file"
	dir := "test_dir"
	newDir := "new_dir"
	// Create:
	// test_dir/
	// test_dir/file
	// test_dir/test_dir
	// test_dir/test_dir/file

	// Then move test_dir - > new_dir
	//
	// Should end up with:
	// new_dir/
	// new_dir/file
	// new_dir/test_dir
	// new_dir/test_dir/file
	cmd := []string{"/bin/sh", "-c", fmt.Sprintf("mkdir %s; echo file > %s/%s; mkdir %s/%s; echo file > %s/%s/%s; mv %s %s", dir, dir, file, dir, dir, dir, dir, file, dir, newDir)}
	res, err := t.RunResolvedCmd(workspace, cmd)
	if err != nil {
		return err
	}

	// Expect name to be in delete set
	if len(res.Deletes) != 1 {
		return fmt.Errorf("unexpected delete count %d", len(res.Deletes))
	}
	// Expect 4 writes from above
	if len(res.Writes) != 4 {
		return fmt.Errorf("unexpected write count %d", len(res.Writes))
	}
	if len(res.Reads) != 0 {
		return fmt.Errorf("unexpected read count %d", len(res.Reads))
	}

	delName := tapName(mntRoot, workspace, dir)

	for k, _ := range res.Deletes {
		if k != delName {
			return fmt.Errorf("Expected delete file %s got %s", delName, k)
		}
	}
	// Expected entries
	expWrites := []string{
		tapName(mntRoot, workspace, newDir),
		tapName(mntRoot, workspace, filepath.Join(newDir, file)),
		tapName(mntRoot, workspace, filepath.Join(newDir, dir)),
		tapName(mntRoot, workspace, filepath.Join(newDir, dir, file)),
	}

	// Just validating path is expected with this test
	for _, k := range expWrites {
		if _, exists := res.Writes[k]; !exists {
			return fmt.Errorf("expected write file %s not found", k)
		}
	}

	return nil

}
