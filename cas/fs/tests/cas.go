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
	iofs "io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/google/abfs/cas/fs"
	"github.com/google/abfs/cas/hash"
)

type casTest struct {
	fs     fs.FS
	hasher hash.Hasher
}

func (c *casTest) MkdirTest(path string) error {
	if err := MkdirTest(path); err != nil {
		return err
	}
	hash, err := c.fs.Hash(path)
	if err != nil {
		return err
	}

	if !hash.IsEmptyDir() {
		return fmt.Errorf("did not get empty dir hash on mkdir %s", hash)
	}
	dirty, err := c.fs.Dirty(path)
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("directory dirty")
	}
	return nil

}
func (c *casTest) MkdirTestStat(path string) error {

	if err := MkdirTest(path); err != nil {
		return err
	}
	hs, err := c.fs.HashStat(path)
	if err != nil {
		return err
	}
	if !hs.Hash.IsEmptyDir() {
		return fmt.Errorf("did not get empty dir hash on mkdir %s", hs.Hash)
	}
	if !hs.Mode.IsDir() {
		return fmt.Errorf("expected directory mode got %o", hs.Mode)
	}
	if hs.Size != 0 {
		return fmt.Errorf("wrong size on stat %d", hs.Size)
	}
	return nil

}
func (c *casTest) FileTest(path string) error {
	if err := FileTest(path); err != nil {
		return err
	}
	hash, err := c.fs.Hash(path)
	if err != nil {
		return err
	}
	if !hash.IsEmptyFile() {
		return fmt.Errorf("did not get empty file hash")
	}

	hash, sz, err := c.fs.HashSize(path)
	if !hash.IsEmptyFile() {
		return fmt.Errorf("did not get empty file hash via hash size")
	}
	if sz != 0 {
		return fmt.Errorf("did not get 0 size")
	}

	parent := filepath.Dir(path)
	dirty, err := c.fs.Dirty(parent)
	if err != nil {
		return err
	}
	if !dirty {
		return fmt.Errorf("parent directory not dirty")
	}
	return nil
}
func (c *casTest) FileTestContent(path, content string) error {
	if err := FileTestContent(path, content); err != nil {
		return err
	}
	hash, err := c.fs.Hash(path)
	if err != nil {
		return err
	}
	if hash.IsEmptyFile() {
		return fmt.Errorf("got empty file hash")
	}

	hash, sz, err := c.fs.HashSize(path)
	if hash.IsEmptyFile() {
		return fmt.Errorf("got empty file hash via hash size")
	}
	if sz != int64(len(content)) {
		return fmt.Errorf("got %d size via hash size expected %d", sz, len(content))
	}

	return nil
}

func (c *casTest) FileTestContentStat(path, content string) error {

	if err := FileTestContent(path, content); err != nil {
		return err
	}
	hs, err := c.fs.HashStat(path)
	if err != nil {
		return err
	}
	if hs.Hash.IsEmptyFile() {
		return fmt.Errorf("got empty file hash")
	}

	if hs.Size != int64(len(content)) {
		return fmt.Errorf("got %d size via hash stat expected %d", hs.Size, len(content))
	}
	if !hs.Mode.IsRegular() {
		return fmt.Errorf("expected regular file got %o", hs.Mode)
	}
	return nil

}
func (c *casTest) FileTestTruncate(path, content string) error {
	if err := FileTestTruncate(path, content); err != nil {
		return err
	}
	hash, err := c.fs.Hash(path)
	if err != nil {
		return err
	}
	if !hash.IsEmptyFile() {
		return fmt.Errorf("got non-empty file hash")
	}
	return nil
}

func (c *casTest) FileTestTruncateOpen(path, content string) error {
	if err := FileTestTruncateOpen(path, content); err != nil {
		return err
	}
	hash, err := c.fs.Hash(path)
	if err != nil {
		return err
	}
	if !hash.IsEmptyFile() {
		return fmt.Errorf("got non-empty file hash")
	}
	return nil
}

func (c *casTest) FileTestCopy(path, cpPath, content string) error {
	if err := FileTestContent(path, content); err != nil {
		return err
	}
	hash, sz, err := c.fs.HashSize(path)
	if err != nil {
		return err
	}
	osMode := 0644
	if err := syscall.Mknod(cpPath, syscall.S_IFREG|uint32(osMode), 0); err != nil {
		return err
	}
	if err := c.fs.SetHashSize(cpPath, hash, sz); err != nil {
		return err
	}

	cpHash, err := c.fs.Hash(cpPath)
	if err != nil {
		return err
	}
	if !hash.Equals(cpHash) {
		return fmt.Errorf("hash mismatch %s %s", hash, cpHash)
	}
	if ValidateFileContent(cpPath, content) != nil {
		return err
	}
	return nil
}

func (c *casTest) SymlinkTest(target, symlink string) error {
	err := SymlinkTest(target, symlink)
	if err != nil {
		return err
	}
	hash, err := c.fs.Hash(target)
	if err != nil {
		return err
	}
	symHash, err := c.fs.Hash(symlink)
	if err != nil {
		return err
	}

	if hash.Equals(symHash) {
		return fmt.Errorf("file and symlink have same hash %s", hash)
	}
	parent := filepath.Dir(target)
	dirty, err := c.fs.Dirty(parent)
	if err != nil {
		return err
	}
	if !dirty {
		return fmt.Errorf("parent directory not dirty")
	}

	return nil
}

func (c *casTest) SymlinkTestStat(target, symlink string) error {

	err := SymlinkTest(target, symlink)
	if err != nil {
		return err
	}
	hs, err := c.fs.HashStat(target)
	if err != nil {
		return err
	}
	sh, err := c.fs.HashStat(symlink)
	if err != nil {
		return err
	}

	if hs.Hash.Equals(sh.Hash) {
		return fmt.Errorf("file and symlink have same hash %s", hs.Hash)
	}
	if (sh.Mode & iofs.ModeSymlink) == 0 {
		return fmt.Errorf("expected symlink got %o", sh.Mode)
	}
	if sh.Size != int64(len(target)) {
		return fmt.Errorf("got %d len via hash stat expected %d", sh.Size, len(target))
	}
	return nil
}
func (c *casTest) SymlinkTestNoSourceStat(target, symlink string) error {
	err := SymlinkTestNoSource(target, symlink)
	if err != nil {
		return err
	}
	sh, err := c.fs.HashStat(symlink)
	if err != nil {
		return err
	}

	if (sh.Mode & iofs.ModeSymlink) == 0 {
		return fmt.Errorf("expected symlink got %o", sh.Mode)
	}
	if sh.Size != int64(len(target)) {
		return fmt.Errorf("got %d len via hash stat expected %d", sh.Size, len(target))
	}

	return nil
}
func (c *casTest) HardLinkFile(target, link string) error {
	err := HardLinkFile(target, link)
	if err != nil {
		return err
	}
	// New file is should be dirty check via link
	dirty, err := c.fs.Dirty(link)
	if err != nil {
		return err
	}
	if !dirty {
		return fmt.Errorf("link file is not dirty")
	}
	// Snapshot the file via link make sure target is clean
	h1, err := c.fs.Hash(link)
	if err != nil {
		return err
	}
	dirty, err = c.fs.Dirty(target)
	if dirty {
		return fmt.Errorf("target is still dirty after hashing via link")
	}

	h2, err := c.fs.Hash(target)
	if err != nil {
		return err
	}
	if !h1.Equals(h2) {
		return fmt.Errorf("target and hard link have different hashes")
	}
	return nil
}
func (c *casTest) DirTestStage(path string) error {
	if err := DirTestStage(path); err != nil {
		return err
	}

	hash, err := c.fs.Hash(path)
	if err != nil {
		return err
	}
	if hash.IsEmptyDir() {
		return fmt.Errorf("got empty dir hash")
	}
	return nil
}

func (c *casTest) DirTestStageCopy(path, cpPath string) error {
	if err := DirTestStage(path); err != nil {
		return err
	}
	hash, err := c.fs.Hash(path)
	if err != nil {
		return err
	}
	if err := MkdirTest(cpPath); err != nil {
		return err
	}
	if err := c.fs.SetHash(cpPath, hash); err != nil {
		return err
	}
	cpHash, err := c.fs.Hash(cpPath)
	if err != nil {
		return err
	}
	if !hash.Equals(cpHash) {
		return fmt.Errorf("hash mismatch %s %s", hash, cpHash)
	}
	if err := ValidateTestStage(cpPath); err != nil {
		return err
	}

	return nil
}

// Check an excluded child does not change the parent until
// set to included
func (c *casTest) checkExcluded(startHash hash.Hash, parent, child string) error {
	if err := c.fs.SetExcluded(child, true); err != nil {
		return err
	}
	hash1, err := c.fs.Hash(parent)
	if err != nil {
		return err
	}
	if !startHash.Equals(hash1) {
		return fmt.Errorf("hashes are not the same")
	}
	if exc, err := c.fs.Excluded(child); err != nil {
		return err
	} else if exc != true {
		return fmt.Errorf("expected excluded but got included")
	}
	if err := c.fs.SetExcluded(child, false); err != nil {
		return err
	}
	if exc, err := c.fs.Excluded(child); err != nil {
		return err
	} else if exc != false {
		return fmt.Errorf("expected included but got excluded")
	}

	hash2, err := c.fs.Hash(parent)
	if err != nil {
		return err
	}
	if startHash.Equals(hash2) {
		return fmt.Errorf("hashes are the same")
	}
	return nil
}
func (c *casTest) ExcludeDirTest(parent, dir string) error {
	// grab hash of parent directory
	// create new directory and exclude it
	// grab hash of parent directory make sure still same
	// unexclude new directory
	// grab hash of parent directory make sure changed
	if err := c.MkdirTest(parent); err != nil {
		return err
	}
	hash, err := c.fs.Hash(parent)
	if err != nil {
		return err
	}
	dirPath := filepath.Join(parent, dir)

	if err := c.MkdirTest(dirPath); err != nil {
		return err
	}
	return c.checkExcluded(hash, parent, dirPath)

}

func (c *casTest) ExcludeFileTest(parent, fname string) error {
	// same as exclude dir but use a file instead
	if err := c.MkdirTest(parent); err != nil {
		return err
	}
	hash, err := c.fs.Hash(parent)
	if err != nil {
		return err
	}
	fPath := filepath.Join(parent, fname)

	if err := c.FileTest(fPath); err != nil {
		return err
	}
	return c.checkExcluded(hash, parent, fPath)

}
func (c *casTest) DebugTests(dir string) error {
	if err := c.MkdirTest(dir); err != nil {
		return err
	}
	if exc, err := c.fs.Debug(dir); err != nil {
		return err
	} else if exc != false {
		return fmt.Errorf("expected non-debug but got debug")
	}
	if err := c.fs.SetDebug(dir, true); err != nil {
		return err
	}
	if exc, err := c.fs.Debug(dir); err != nil {
		return err
	} else if exc != true {
		return fmt.Errorf("expected debug got non-debug")
	}
	if err := c.fs.SetDebug(dir, false); err != nil {
		return err
	}

	if exc, err := c.fs.Debug(dir); err != nil {
		return err
	} else if exc != false {
		return fmt.Errorf("expected non-debug but got debug")
	}
	return nil
}

// Hash coverage tests
func (c *casTest) HashTests() error {
	// Expect unique hashes for empty dir, file, and zero
	zero := c.hasher.ZeroHash()
	emptyDir := c.hasher.EmptyDirHash()
	emptyFile := c.hasher.EmptyFileHash()

	if emptyDir.IsZero() {
		return fmt.Errorf("empty dir hash is zero")
	}
	if emptyFile.IsZero() {
		return fmt.Errorf("empty file hash is zero")
	}
	if !zero.IsZero() {
		return fmt.Errorf("zero hash is not zero")
	}

	if emptyDir.Equals(emptyFile) {
		return fmt.Errorf("empty dir hash equals empty file hash")
	}

	var buf bytes.Buffer
	n, err := emptyFile.Write(&buf)

	if err != nil {
		return err
	}

	newHash := c.hasher.ZeroHash()
	n2, err := newHash.Read(&buf)
	if err != nil {
		return fmt.Errorf("failed to read hash")
	}
	if n != n2 {
		return fmt.Errorf("wrote %d bytes read %d", n, n2)
	}
	if !emptyFile.Equals(newHash) {
		return fmt.Errorf("reading hash via io returned unexpected hash")
	}
	// Create junk hash
	if _, err := c.hasher.NewHash("notahash"); err == nil {
		return fmt.Errorf("expected error on bad hash value got success")
	}

	if _, err := c.hasher.NewHash(strings.Repeat("Z", n*2)); err == nil {
		return fmt.Errorf("expected error on bad hash value got success")
	}
	return nil
}

func (c *casTest) ARISizeTest(fpath, ari string, size int64, expectedPayload []byte) error {
	f, err := os.OpenFile(fpath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to create ARI test file: %w", err)
	}
	f.Close()

	if err := c.fs.SetARISize(fpath, ari, size); err != nil {
		return fmt.Errorf("SetARISize failed: %w", err)
	}

	readAri, readSize, err := c.fs.ARISize(fpath)
	if err != nil {
		return fmt.Errorf("ARISize read failed: %w", err)
	}
	if readAri != ari {
		return fmt.Errorf("ARI mismatch: expected %q, got %q", ari, readAri)
	}
	if readSize != size {
		return fmt.Errorf("Size mismatch: expected %d, got %d", size, readSize)
	}

	data, err := os.ReadFile(fpath)
	if err != nil {
		return fmt.Errorf("failed to read content back from ARI file: %w", err)
	}
	if !bytes.Equal(data, expectedPayload) {
		return fmt.Errorf("ARI content mismatch: expected %q, got %q", string(expectedPayload), string(data))
	}

	return nil
}

func (c *casTest) ARIWriteAndSnapshotTest(fpath, ari string, origPayload, newPayload []byte) error {
	f, err := os.OpenFile(fpath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	f.Close()

	if err := c.fs.SetARISize(fpath, ari, int64(len(origPayload))); err != nil {
		return fmt.Errorf("SetARISize failed: %w", err)
	}

	fWrite, err := os.OpenFile(fpath, os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open ARI file for write: %w", err)
	}
	if _, err := fWrite.Write(newPayload); err != nil {
		fWrite.Close()
		return fmt.Errorf("failed to write new payload: %w", err)
	}
	fWrite.Close()

	readData, err := os.ReadFile(fpath)
	if err != nil {
		return fmt.Errorf("failed to read back modified content: %w", err)
	}
	if !bytes.Equal(readData, newPayload) {
		return fmt.Errorf("content mismatch: expected %q, got %q", string(newPayload), string(readData))
	}

	if _, _, err := c.fs.ARISize(fpath); err == nil {
		return fmt.Errorf("expected ARISize to fail on dirty/modified file, but it succeeded")
	}

	h, err := c.fs.Hash(fpath)
	if err != nil {
		return fmt.Errorf("Hash snapshot failed post-write: %w", err)
	}
	if h.IsEmptyFile() || h.IsZero() {
		return fmt.Errorf("expected valid hash post-write snapshot, got empty/zero: %s", h)
	}
	return nil
}

func (c *casTest) ARISetOnHashedFileTest(fpath, content, ari string) error {
	if err := FileTestContent(fpath, content); err != nil {
		return err
	}

	h, sz, err := c.fs.HashSize(fpath)
	if err != nil {
		return fmt.Errorf("failed to get HashSize for baseline file: %w", err)
	}
	if h.IsEmptyFile() {
		return fmt.Errorf("expected non-empty hash for populated file")
	}

	if err := c.fs.SetARISize(fpath, ari, sz); err != nil {
		return fmt.Errorf("failed to SetARISize on a hashed file: %w", err)
	}

	return nil
}

func (c *casTest) ARISetSameARITest(fpath, ari string, size int64) error {
	f, err := os.OpenFile(fpath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to create test file: %w", err)
	}
	f.Close()

	if err := c.fs.SetARISize(fpath, ari, size); err != nil {
		return fmt.Errorf("initial SetARISize failed: %w", err)
	}

	if err := c.fs.SetARISize(fpath, ari, size); err != nil {
		return fmt.Errorf("re-setting the same ARI failed: %w", err)
	}

	readAri, readSize, err := c.fs.ARISize(fpath)
	if err != nil {
		return fmt.Errorf("ARISize read back failed: %w", err)
	}
	if readAri != ari || readSize != size {
		return fmt.Errorf("readback mismatch after re-setting same ARI: got %s:%d, expected %s:%d", readAri, readSize, ari, size)
	}

	return nil
}

func (c *casTest) ARISetDifferentARITest(fpath, ari1, ari2 string, size int64) error {
	f, err := os.OpenFile(fpath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to create test file: %w", err)
	}
	f.Close()

	if err := c.fs.SetARISize(fpath, ari1, size); err != nil {
		return fmt.Errorf("initial SetARISize failed: %w", err)
	}

	if err := c.fs.SetARISize(fpath, ari2, size); err != nil {
		return fmt.Errorf("failed to SetARISize with a different ARI: %w", err)
	}

	return nil
}

func (c *casTest) ARISetHashOnARIFileTest(fpath, ari string, size int64) error {
	f, err := os.OpenFile(fpath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to create test file: %w", err)
	}
	f.Close()

	if err := c.fs.SetARISize(fpath, ari, size); err != nil {
		return fmt.Errorf("initial SetARISize failed: %w", err)
	}

	placeholderHash := c.hasher.EmptyFileHash()
	if err := c.fs.SetHashSize(fpath, placeholderHash, size); err != nil {
		return fmt.Errorf("SetHashSize failed on an ARI-backed file: %w", err)
	}

	return nil
}

func (c *casTest) BackendStatARITest(fpath, ari string, size int64, payload []byte, expectedMode iofs.FileMode) error {
	f, err := os.OpenFile(fpath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, expectedMode)
	if err != nil {
		return fmt.Errorf("failed to create ARI test file: %w", err)
	}
	f.Close()

	if err := c.fs.SetARISize(fpath, ari, size); err != nil {
		return fmt.Errorf("SetARISize failed: %w", err)
	}

	stat, err := c.fs.BackendStat(fpath)
	if err != nil {
		return fmt.Errorf("BackendStat failed on ARI file: %w", err)
	}

	if !stat.IsARI {
		return fmt.Errorf("expected IsARI to be true, got false")
	}
	if stat.ARI != ari {
		return fmt.Errorf("ARI mismatch: expected %q, got %q", ari, stat.ARI)
	}
	if stat.Size != size {
		return fmt.Errorf("Size mismatch: expected %d, got %d", size, stat.Size)
	}
	if stat.Mode != expectedMode {
		return fmt.Errorf("Mode mismatch: expected %v, got %v", expectedMode, stat.Mode)
	}

	return nil
}

func (c *casTest) BackendStatHashedFileTest(fpath, content string, expectedMode iofs.FileMode) error {
	if err := FileTestContent(fpath, content); err != nil {
		return err
	}

	if err := os.Chmod(fpath, expectedMode); err != nil {
		return fmt.Errorf("failed to chmod test file: %w", err)
	}

	expectedHash, expectedSize, err := c.fs.HashSize(fpath)
	if err != nil {
		return fmt.Errorf("HashSize failed: %w", err)
	}

	stat, err := c.fs.BackendStat(fpath)
	if err != nil {
		return fmt.Errorf("BackendStat failed on hashed file: %w", err)
	}

	if stat.IsARI {
		return fmt.Errorf("expected IsARI to be false on standard hashed file, got true")
	}
	if !stat.Hash.Equals(expectedHash) {
		return fmt.Errorf("Hash mismatch: expected %s, got %s", expectedHash, stat.Hash)
	}
	if stat.Size != expectedSize {
		return fmt.Errorf("Size mismatch: expected %d, got %d", expectedSize, stat.Size)
	}
	if stat.Mode != expectedMode {
		return fmt.Errorf("Mode mismatch: expected %v, got %v", expectedMode, stat.Mode)
	}

	return nil
}

func (c *casTest) BackendStatDirtyARIFileTest(fpath, ari string, origPayload, newPayload []byte, expectedMode iofs.FileMode) error {
	f, err := os.OpenFile(fpath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, expectedMode)
	if err != nil {
		return fmt.Errorf("failed to create ARI test file: %w", err)
	}
	f.Close()

	if err := c.fs.SetARISize(fpath, ari, int64(len(origPayload))); err != nil {
		return fmt.Errorf("SetARISize failed: %w", err)
	}

	fWrite, err := os.OpenFile(fpath, os.O_WRONLY|os.O_TRUNC, expectedMode)
	if err != nil {
		return fmt.Errorf("failed to open ARI file for writing: %w", err)
	}
	if _, err := fWrite.Write(newPayload); err != nil {
		fWrite.Close()
		return fmt.Errorf("failed to write new payload: %w", err)
	}
	fWrite.Close()

	stat, err := c.fs.BackendStat(fpath)
	if err != nil {
		return fmt.Errorf("BackendStat failed on dirty ARI file: %w", err)
	}

	if stat.IsARI {
		return fmt.Errorf("expected dirty file to transition away from ARI, but BackendStat returned IsARI = true")
	}
	if stat.Hash.IsEmptyFile() || stat.Hash.IsZero() {
		return fmt.Errorf("expected valid non-empty content hash after snapshot, got: %s", stat.Hash)
	}
	if stat.Size != int64(len(newPayload)) {
		return fmt.Errorf("expected updated size %d, got %d", len(newPayload), stat.Size)
	}
	if stat.Mode != expectedMode {
		return fmt.Errorf("Mode mismatch: expected %v, got %v", expectedMode, stat.Mode)
	}

	return nil
}
