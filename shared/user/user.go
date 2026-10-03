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

package user

import (
	"fmt"
	"os"
	osuser "os/user"
	"strconv"
)

// CurrentUser returns the current user. If os/user.Current() fails (e.g. in a statically
// linked binary in an environment where user information is resolved via NSS/LDAP rather
// than /etc/passwd), it falls back to inspecting environment variables ($USER, $LOGNAME,
// $USERNAME) and standard os functions (os.Getuid, os.Getgid, os.UserHomeDir).
func CurrentUser() (*osuser.User, error) {
	u, err := osuser.Current()
	if err == nil {
		return u, nil
	}

	username := os.Getenv("USER")
	if username == "" {
		username = os.Getenv("LOGNAME")
	}
	if username == "" {
		username = os.Getenv("USERNAME")
	}
	if username == "" {
		return nil, fmt.Errorf("could not determine current user: %w (USER/LOGNAME/USERNAME environment variables not set)", err)
	}

	homeDir, _ := os.UserHomeDir()
	if homeDir == "" {
		homeDir = os.Getenv("HOME")
	}

	return &osuser.User{
		Uid:      strconv.Itoa(os.Getuid()),
		Gid:      strconv.Itoa(os.Getgid()),
		Username: username,
		Name:     username,
		HomeDir:  homeDir,
	}, nil
}
