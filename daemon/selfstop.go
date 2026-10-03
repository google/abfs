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
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sync"

	"github.com/fsnotify/fsnotify"

	"github.com/google/abfs/shared/entities"
)

type SelfStoppingServer struct {
	http.Server
	lock         sync.Mutex
	closeOnce    sync.Once
	SocketPath   string
	Done         chan struct{}
	needShutdown bool
}

func (s *SelfStoppingServer) WatchSocket(ctx context.Context) (retErr error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer entities.CloseErrCheck(watcher, &retErr)

	if err := watcher.Add(s.SocketPath); err != nil {
		return err
	}

	for {
		select {
		case <-s.Done:
			return nil
		case event, ok := <-watcher.Events:
			if !ok {
				return fmt.Errorf("events channel closed for %v", s.SocketPath)
			}

			// Another process unlinking the file can show up as Chmod.
			if event.Has(fsnotify.Remove) || event.Has(fsnotify.Chmod) {
				if _, err := os.Stat(event.Name); err != nil && entities.IsNotExist(err) {
					log.Printf("Socket file %q was removed. Shutting down this server.", s.SocketPath)
					return s.Shutdown(ctx)
				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return fmt.Errorf("errors channel closed for %v", s.SocketPath)
			}
			return err
		}
	}
}

func (s *SelfStoppingServer) Serve(l net.Listener) error {
	s.needShutdown = true
	go func() {
		err := s.WatchSocket(context.Background())
		if err != nil {
			log.Printf("WatchSocket error: %v", err)
		}
	}()
	return s.Server.Serve(l)
}

func (s *SelfStoppingServer) Shutdown(ctx context.Context) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	defer s.closeOnce.Do(func() {
		close(s.Done)
	})

	if !s.needShutdown {
		return nil
	}

	s.needShutdown = false

	return s.Server.Shutdown(ctx)
}
