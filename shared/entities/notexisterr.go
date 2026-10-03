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

package entities

import (
	"errors"
	"fmt"
	"io/fs"
	"syscall"
)

var ErrNotExist = fs.ErrNotExist

type NotExistErr struct {
	err error
}

func (e *NotExistErr) Error() string {
	return e.err.Error()
}

func (e *NotExistErr) Unwrap() []error {
	return []error{e.err}
}

func WrapNotExist(err error) error {
	if err == nil {
		return nil
	}

	return &NotExistErr{err: err}
}

func NotExistErrf(format string, a ...any) error {
	return WrapNotExist(fmt.Errorf(format, a...))
}

func IsNotExist(err error) bool {
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}

	var errno syscall.Errno
	if errors.As(err, &errno); errno == syscall.ENOENT {
		return true
	}

	var notExistErr *NotExistErr
	if errors.As(err, &notExistErr) {
		return true
	}

	if errors.Is(err, ErrEntryNotFound) {
		return true
	}
	return false
}

func IgnoreNotExistErr(err error) error {
	if IsNotExist(err) {
		return nil
	}
	return err
}
