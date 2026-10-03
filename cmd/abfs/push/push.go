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

package push

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/google/abfs/casgit"
	"github.com/google/abfs/shared/entities"

	shellgit "github.com/google/abfs/git"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Pusher writes an object to storage.
type Pusher struct {
	repo  *git.Repository
	store entities.CASStore
}

func NewPusher(repo *git.Repository, store entities.CASStore) *Pusher {
	return &Pusher{
		repo:  repo,
		store: store,
	}
}

func (p *Pusher) Push(obj *shellgit.ObjectTypeName) (retErr error) {
	encoded, err := p.repo.Storer.EncodedObject(obj.ObjType, plumbing.Hash(obj.Name))
	if err != nil {
		return fmt.Errorf("obj.Name: %s, err: %w", obj.Name, err)
	}
	reader, err := encoded.Reader()
	if err != nil {
		return err
	}
	defer entities.CloseErrCheck(reader, &retErr)

	switch obj.ObjType {
	case plumbing.BlobObject:
		if err := p.store.WriteBlob(context.Background(), obj.Name, encoded.Size(), reader); err != nil {
			return err
		}
	case plumbing.TreeObject:
		tree, err := object.DecodeTree(p.repo.Storer, encoded)
		if err != nil {
			return err
		}
		sizes := make([]int64, len(tree.Entries))
		for i, ent := range tree.Entries {
			// there might be invalid git mode 100744, so we couldn't use IsFile() to check
			if ent.Mode != filemode.Dir && ent.Mode != filemode.Submodule {
				size, err := p.repo.Storer.EncodedObjectSize(ent.Hash)
				if err != nil {
					return fmt.Errorf("failed to retrieve blob size of %s:%s: %w", ent.Name, ent.Hash, err)
				}
				sizes[i] = size
			} // If not a file default to zero
		}
		buf, err := io.ReadAll(objectReader(encoded, reader))
		if err != nil {
			return err
		}
		if err := p.store.PutTree(context.Background(), obj.Name, buf, entities.TreeMeta{Sizes: sizes}); err != nil {
			return err
		}

	case plumbing.CommitObject:
		multiReader := objectReader(encoded, reader)

		if err := p.store.WriteObject(context.Background(), obj.Name, multiReader); err != nil {
			return err
		}
	}

	return nil
}

func objectReader(encoded plumbing.EncodedObject, reader io.ReadCloser) io.Reader {
	header := bytes.NewBufferString(
		casgit.EncodedObjectHeader(encoded),
	)
	multiReader := io.MultiReader(header, reader)
	return multiReader
}
