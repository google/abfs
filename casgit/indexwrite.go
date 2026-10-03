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

package casgit

import (
	"context"
	"path"
	"path/filepath"

	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects/gitobjects"
)

func (g *CASGit) WriteIndexFromCASFS(workDir string, commitHash entities.Hash, indexFilePath string) error {
	dotgit := path.Dir(indexFilePath)
	dotGitParent, dotGitName := filepath.Dir(dotgit), filepath.Base(dotgit)
	return bindcas.CreateGitIndex(dotGitParent, dotGitName, workDir, commitHash.String())
}

func (g *CASGit) WriteIndexFromDeepTree(workDir string, deepTree *gitobjects.DeepTreeObject, indexFilePath string) error {
	dotgit := path.Dir(indexFilePath)
	dotGitParent, dotGitName := filepath.Dir(dotgit), filepath.Base(dotgit)

	treeRoot := NewDeepTreeDir(g, deepTree)
	return bindcas.CreateGitIndexFromTree(context.Background(), dotGitParent, dotGitName, workDir, treeRoot)
}
