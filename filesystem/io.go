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

package filesystem

import (
	"context"
	"io"
	"os"
	"path"

	"github.com/go-git/go-git/v5/utils/ioutil"

	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
)

func WriteFile(ctx context.Context, root bindcas.Dir, name string, reader io.Reader) (returnErr error) {
	dirName, fileName := path.Split(name)
	dir, err := root.MkdirAll(ctx, dirName)
	if err != nil {
		return err
	}
	f, err := bindcas.OpenFile(path.Join(dir.Path(), fileName), os.O_CREATE|os.O_WRONLY, entities.FileModeRegular)
	if err != nil {
		return err
	}
	defer ioutil.CheckClose(f, &returnErr)

	_, err = io.Copy(io.NewOffsetWriter(f, 0), reader)
	return err
}
