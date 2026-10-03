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

package git

import (
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/google/abfs/cas/cas"
	"github.com/google/abfs/cas/hash"

	"github.com/google/abfs/casfuse"
	"github.com/google/abfs/casfuse/fs"
	"github.com/google/abfs/casfuse/fs/mem"
)

type objectDir struct {
	fs.Dir
}

func newObjectDir(parent fs.FsDir) *objectDir {
	out := &objectDir{
		Dir: *fs.NewDir(parent, cas.FileModeDir(), mem.NewNodeFactory(parent)),
	}
	return out
}
func newObjectFile(d *objectDir, hash hash.Hash) (fs.Node, error) {
	gitfs := GetGit(d.FS())
	info, err := gitfs.GetPath(hash)
	if err != nil {
		return nil, err
	}
	blob := newBlob(info)
	// do internal open here so that we can have the right file size
	// for the existance of the file
	if err := blob.doOpen(); err != nil {
		return nil, err
	}
	// create file with blob
	f := fs.NewFile(d, cas.FileModeFile(), blob)
	return f, nil

}
func (d *objectDir) isObjName(dentry casfuse.Dentry) (hash.Hash, bool) {
	gitfs := GetGit(d.FS())
	if dentry.Parent() == nil {
		return nil, false
	}
	hashName := fmt.Sprintf("%s%s", dentry.Parent().Name(), dentry.Name())

	hash, err := gitfs.Hasher().NewHash(hashName)
	if err != nil {
		return nil, false
	}
	return hash, true
}
func (d *objectDir) Lookup(ctx context.Context, dentry casfuse.Dentry) (casfuse.Inode, error) {
	// Need to do lookup / create all within the lock
	d.Lock()
	defer d.Unlock()

	entries := d.Dir.InternalEntriesLocked()

	node, exists := (*entries)[dentry.Name()]
	if exists {
		return node, nil
	}

	if objHash, ok := d.isObjName(dentry); ok {
		out, err := newObjectFile(d, objHash)
		if err != nil {
			return nil, err
		}
		// strip parent directory name from file name
		name := objHash.String()[len(dentry.Parent().Name()):]
		d.Dir.AddChild(name, out)
		return out, nil

	}
	return nil, casfuse.ErrEntryNotFound
}
func (d *objectDir) Rename(ctx context.Context, oldDentry casfuse.Dentry, newDirInode casfuse.Inode, newDentry casfuse.Dentry) error {
	d.Lock()
	entries := d.Dir.InternalEntriesLocked()
	hashName := fmt.Sprintf("%s%s", newDentry.Parent().Name(), newDentry.Name())
	gitfs := GetGit(d.FS())
	key, err := gitfs.Hasher().NewHash(hashName)
	if err != nil {
		d.Unlock()
		return d.Dir.Rename(ctx, oldDentry, newDirInode, newDentry)
	}
	defer d.Unlock()
	node, exists := (*entries)[oldDentry.Name()]
	if !exists {
		return casfuse.ErrEntryNotFound
	}

	file, ok := node.(*fs.File)
	if !ok {
		return casfuse.ErrNotAFile
	}
	blob := file.Blob()
	buf := blob.(*mem.Blob).Data()

	var writer func(ctx context.Context, key hash.Hash, reader io.Reader) error

	objType, err := peekObjectType(buf)
	if err != nil {
		return err
	}

	var reader io.Reader
	if *objType == GitTypeBlob {
		zz, err := zlib.NewReader(bytes.NewReader(buf))
		if err != nil {
			return err
		}
		defer zz.Close()

		blReader := blobReader{zlibReader: zz}
		reader = &blReader
		writer = gitfs.WriteBlob
	} else {
		// This pays the penalty of uncompressing here and then re-compressing
		// on the cas store end. The cas store API surface needs to grow to
		// mitigate this.
		zz, err := zlib.NewReader(bytes.NewReader(buf))
		if err != nil {
			return err
		}
		defer zz.Close()

		reader = zz
		writer = gitfs.WriteObject
	}

	if err := writer(ctx, key, reader); err != nil {
		return err
	}
	// Remove
	delete(*entries, oldDentry.Name())
	return nil
}

func peekObjectType(buf []byte) (*GitType, error) {
	zz, err := zlib.NewReader(bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	defer zz.Close()

	dst := make([]byte, 1)
	_, err = zz.Read(dst)
	if err != nil {
		return nil, err
	}
	var out GitType
	switch dst[0] {
	case 'b':
		out = GitTypeBlob
	case 't':
		out = GitTypeTree
	case 'c':
		out = GitTypeCommit
	default:
		return nil, errors.New("unknown object type")
	}
	return &out, nil
}

// Converts a compressed blob with header information to an uncompressed
// blob without header information.
type blobReader struct {
	zlibReader io.Reader
	headerSeen bool
}

func (b *blobReader) Read(p []byte) (n int, err error) {
	if !b.headerSeen {
		buf := make([]byte, len(p))
		for {
			sz, err := b.zlibReader.Read(buf)
			if err != nil {
				if !errors.Is(err, io.EOF) {
					return 0, err
				}
			}
			zero := bytes.IndexByte(buf, 0)
			if zero > 0 {
				n = copy(p, buf[zero+1:sz])
				b.headerSeen = true
				p = p[:n]
				return n, err
			}
		}
	}
	return b.zlibReader.Read(p)
}
