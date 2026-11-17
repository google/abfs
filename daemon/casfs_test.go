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

package daemon

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/filesystem"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
)

// unmountingMount serves like the kmod mount: Serve returns once Unmount has started, and
// Unmount returns only when release is closed.
type unmountingMount struct {
	filesystem.HTTPMount
	unmounting chan struct{}
	release    chan struct{}
	unmounted  chan struct{}
}

func (m *unmountingMount) SetInitConfig(*entities.FSConfig) {}

func (m *unmountingMount) Config() *entities.FSConfig {
	return nil
}

func (m *unmountingMount) Mount(context.Context, string, filesystem.MountInfo, entities.CASStore) error {
	return nil
}

func (m *unmountingMount) Chroot(context.Context, string) (bindcas.Dir, error) {
	return nil, errors.New("not implemented")
}

func (m *unmountingMount) Ready() {}

func (m *unmountingMount) CASFS() bindcas.CASFS {
	return nil
}

func (m *unmountingMount) CreateDir(string) bindcas.Dir {
	return nil
}

func (m *unmountingMount) Serve() error {
	<-m.unmounting
	return nil
}

func (m *unmountingMount) Unmount() {
	close(m.unmounting)
	<-m.release
	close(m.unmounted)
}

type failingStore struct {
	entities.CASStore
}

func (s *failingStore) PutObject(context.Context, entities.Hash, []byte) error {
	return errors.New("not implemented")
}

func TestCASFSServe_ReturnsAfterUnmount(t *testing.T) {
	// Serve registers its handlers on the default mux.
	origMux := http.DefaultServeMux
	http.DefaultServeMux = http.NewServeMux()
	defer func() { http.DefaultServeMux = origMux }()

	mnt := &unmountingMount{
		unmounting: make(chan struct{}),
		release:    make(chan struct{}),
		unmounted:  make(chan struct{}),
	}
	mounted := make(chan struct{})
	c := &casfs{
		mountPoint:      t.TempDir(),
		mnt:             mnt,
		store:           &failingStore{},
		mountDetails:    &binding.MountDetails{},
		mountedCallback: func() { close(mounted) },
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- c.Serve(ctx) }()

	<-mounted
	cancel()
	<-mnt.unmounting
	select {
	case <-served:
		t.Fatal("Serve returned before the unmount finished")
	case <-time.After(100 * time.Millisecond):
	}

	close(mnt.release)
	select {
	case err := <-served:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after the unmount finished")
	}
	select {
	case <-mnt.unmounted:
	default:
		t.Fatal("Serve returned before the unmount finished")
	}
}
