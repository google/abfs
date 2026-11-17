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

package ws

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUnmountAfterFailure(t *testing.T) {
	cause := errors.New("repo sync failed: exit status 1")
	tests := []struct {
		name     string
		mountErr error
		mounted  bool
		hangs    bool
		want     string
	}{
		{name: "unmounted", want: "repo sync failed: exit status 1; unmounted /mnt/ws again"},
		{name: "still mounted", mounted: true, want: "repo sync failed: exit status 1; failed to unmount /mnt/ws: it is still mounted"},
		{name: "mount error, unmounted", mountErr: errors.New("exit status 2"), want: "repo sync failed: exit status 1; unmounted /mnt/ws again"},
		{name: "mount error, still mounted", mountErr: errors.New("exit status 2"), mounted: true, want: "repo sync failed: exit status 1; failed to unmount /mnt/ws: exit status 2"},
		{name: "still unmounting", hangs: true, want: "repo sync failed: exit status 1; /mnt/ws is still unmounting after 50ms"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.hangs {
				defer func(d time.Duration) { mountEndTimeout = d }(mountEndTimeout)
				mountEndTimeout = 50 * time.Millisecond
			}
			// Like c.done, done delivers the result of the mount only once it has ended, some time after stop.
			stopped := make(chan struct{})
			done := make(chan error)
			var ending atomic.Bool
			go func() {
				<-stopped
				if tt.hangs {
					return
				}
				time.Sleep(50 * time.Millisecond)
				ending.Store(true)
				done <- tt.mountErr
			}()
			stop := func() { close(stopped) }
			var endedFirst bool
			var checked string
			isMounted := func(dir string) bool {
				endedFirst = ending.Load()
				checked = dir
				return tt.mounted
			}

			result := make(chan error, 1)
			go func() { result <- unmountAfterFailure(stop, done, "/mnt/ws", isMounted, cause) }()
			var err error
			select {
			case err = <-result:
			case <-time.After(10 * time.Second):
				t.Fatal("waits for a mount it never stopped")
			}
			require.EqualError(t, err, tt.want)
			require.ErrorIs(t, err, cause)
			if tt.hangs {
				require.Empty(t, checked, "checked the mount point of a mount that has not ended")
				return
			}
			require.True(t, endedFirst, "checked the mount point before the mount had ended")
			require.Equal(t, "/mnt/ws", checked)
			if tt.mountErr != nil && tt.mounted {
				require.ErrorIs(t, err, tt.mountErr)
			}
		})
	}
}
