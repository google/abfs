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

package dagsync

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/shared/objects/gitobjects"
	"github.com/google/abfs/shared/remote"
)

type Callback func(err error)

type Request interface {
	Links(accessor ObjectAccessor) ([]entities.Hash, error)
	Execute(ctx context.Context, updater ABFSUpdater, accessor ObjectAccessor) error
	Type() string
	String() string
	Key() entities.Hash
}

type PutObjectRequest struct {
	hash    entities.Hash
	objType entities.ObjectType
}

func (r *PutObjectRequest) IsBlob() bool {
	return r.objType == entities.ObjectTypeBlob
}
func (r *PutObjectRequest) Key() entities.Hash {
	return r.hash
}
func (r *PutObjectRequest) objectLinks(accessor ObjectAccessor) ([]entities.Hash, error) {
	content, err := accessor.GetObject(context.Background(), r.hash)
	if err != nil {
		return nil, err
	}
	t, obj, err := objects.Factory.Decode(r.hash, content)
	if err != nil {
		return nil, fmt.Errorf("failed to decode: %w", err)
	}
	r.objType = t

	return obj.Links(), nil
}
func (r *PutObjectRequest) Links(accessor ObjectAccessor) ([]entities.Hash, error) {
	if r.IsBlob() {
		return nil, nil
	}
	links, err := r.objectLinks(accessor)
	if err == nil { // No errors
		return links, nil
	}

	if !entities.IsNotExist(err) {
		return nil, err
	}

	// Fallback to blob if links for objects failed
	reader, err := accessor.OpenBlob(context.Background(), r.hash)
	if err == nil { // No errors
		r.objType = entities.ObjectTypeBlob
		err = reader.Close()
	}

	if entities.IsNotExist(err) {
		return nil, entities.NotExistErrf("could not find object %s to discover the links", r.hash)
	}

	return nil, err
}
func (r *PutObjectRequest) putBlob(ctx context.Context, updater ABFSUpdater, accessor ObjectAccessor) (retErr error) {
	readSizer, err := accessor.OpenBlob(ctx, r.hash)
	if err != nil {
		return err
	}
	size, err := readSizer.Size()
	if err != nil {
		if err := readSizer.Close(); err != nil {
			return fmt.Errorf("failed to close blob reader: %w", err)
		}
		return err
	}
	return updater.PutObject(ctx, r.hash, remote.NewBlobReader(readSizer, size))
	// Deleted the code where the blob bytes were all read into memory to calculate the size
	// for the header. We are now enforcing the size is available from the accessor. Reading the
	// bytes into memory was causing massive memory usage in some cases.
}
func (r *PutObjectRequest) Execute(ctx context.Context, updater ABFSUpdater, accessor ObjectAccessor) error {
	if r.IsBlob() {
		return r.putBlob(ctx, updater, accessor)
	}

	content, err := accessor.GetObject(context.Background(), r.hash)
	if err != nil {
		return err
	}
	return putObject(ctx, updater, r.hash, content)
}
func (r *PutObjectRequest) Type() string {
	// If an object's type is initially unknown, its type is considered as "object".
	// If the type is later discovered to be a tree or a blob, the counters are updated in handleRequestLinked.

	switch r.objType {
	// blob and tree (dir) are the only 2 object types that cacheman reports on separately
	case entities.ObjectTypeBlob:
		return "blob"
	case gitobjects.ObjectTypeDir:
		return "tree"
	default:
		return "object"
	}
}
func (r *PutObjectRequest) String() string {
	return r.hash.String()
}

func putObject(ctx context.Context, updater ABFSUpdater, hash entities.Hash, content []byte) error {
	return updater.PutObject(ctx, hash, remote.ContentReaderFromBytes(content))
}

// NewObjectRequest creates an Object request based on the provided type
func NewObjectRequest(hash entities.Hash) Request {
	return &PutObjectRequest{hash: hash}
}

type AddMapRequest struct {
	from entities.Hash
	to   entities.Hash
}

func (r *AddMapRequest) Key() entities.Hash {
	return r.from
}
func (r *AddMapRequest) Links(accessor ObjectAccessor) ([]entities.Hash, error) {
	return []entities.Hash{r.from, r.to}, nil
}
func (r *AddMapRequest) Execute(ctx context.Context, updater ABFSUpdater, accessor ObjectAccessor) error {
	return updater.AddMapResult(ctx, r.from, r.to)
}
func (r *AddMapRequest) Type() string {
	return "map"
}
func (r *AddMapRequest) String() string {
	return fmt.Sprintf("map:%s->%s", r.from, r.to)
}

type PutRefsRequest struct {
	server  string
	project string
	refs    []entities.RefHash
}

func (r *PutRefsRequest) Key() entities.Hash {
	// TODO: Key() is ok being empty for some use cases in dagsync, but not for others (needed for marking skipped nodes)
	return entities.Hash{}
}
func (r *PutRefsRequest) Links(accessor ObjectAccessor) ([]entities.Hash, error) {
	links := make([]entities.Hash, 0, len(r.refs))
	for _, ref := range r.refs {
		// zero hash for a ref means it is getting soft deleted
		if ref.Hash.IsZero() {
			continue
		}
		links = append(links, ref.Hash)
	}
	return links, nil
}
func (r *PutRefsRequest) Execute(ctx context.Context, updater ABFSUpdater, accessor ObjectAccessor) error {
	return updater.PutRefs(ctx, r.server, r.project, r.refs)
}
func (r *PutRefsRequest) Type() string {
	return "ref"
}

const maxRefsString = 10

func (r *PutRefsRequest) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "refs:%s,%s:", r.server, r.project)
	for i, ref := range r.refs[:min(len(r.refs), maxRefsString)] {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%s->%s", ref.Ref, ref.Hash)
	}
	if len(r.refs) > maxRefsString {
		fmt.Fprintf(&b, ",(%d total refs...)", len(r.refs))
	}
	return b.String()
}

type PhonyRequest struct {
	links []entities.Hash
}

func (r *PhonyRequest) Key() entities.Hash {
	// TODO: Key() is ok being empty for some use cases in dagsync, but not for others (needed for marking skipped nodes)
	return entities.Hash{}
}
func (r *PhonyRequest) Links(_ ObjectAccessor) ([]entities.Hash, error) {
	return r.links, nil
}
func (r *PhonyRequest) Execute(ctx context.Context, updater ABFSUpdater, accessor ObjectAccessor) error {
	headers, err := updater.Headers(ctx, r.links)
	if err != nil {
		return err
	}
	missing := make([]entities.Hash, 0, len(r.links))
	for i, h := range headers {
		// Headers returns an empty type for missing objects
		if h.IsEmpty() {
			missing = append(missing, r.links[i])
		}
	}
	if len(missing) > 0 {
		// we need to return this error struct for dagsync to re-push these missing links
		return &remote.ErrObjectLinkValidation{
			MissingLinks: missing,
		}
	}
	return nil
}
func (r *PhonyRequest) Type() string {
	return "phony"
}

func (r *PhonyRequest) String() string {
	var b strings.Builder
	b.WriteString("phony:")
	for i, l := range r.links {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(l.String())
	}
	return b.String()
}

var requestsTypes []Request = []Request{
	&PutObjectRequest{},
	// blob and tree (dir) are the only 2 object types cacheman reports on separately
	&PutObjectRequest{objType: entities.ObjectTypeBlob},
	&PutObjectRequest{objType: gitobjects.ObjectTypeDir},
	&AddMapRequest{},
	&PutRefsRequest{},
	&PhonyRequest{},
}
