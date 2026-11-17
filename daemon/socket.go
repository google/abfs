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
	"fmt"
	"net"
	"os"
	"time"

	"github.com/google/abfs/shared/entities"
)

const socketDialTimeout = time.Second

// NeedListener reports whether no server answers on the unix socket at p. It removes a socket
// file that no server answers, so that a new listener can bind to p.
func NeedListener(p string) (bool, error) {
	st, err := os.Lstat(p)
	if entities.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if st.Mode()&os.ModeSocket == 0 {
		return false, fmt.Errorf("%s is not a unix domain socket", p)
	}
	if conn, err := net.DialTimeout("unix", p, socketDialTimeout); err == nil {
		return false, conn.Close()
	}
	if err := os.Remove(p); err != nil && !entities.IsNotExist(err) {
		return false, fmt.Errorf("failed to remove stale socket %s: %w", p, err)
	}
	return true, nil
}
