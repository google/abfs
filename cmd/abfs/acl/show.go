// Copyright 2025 Google LLC
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

package acl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/hashpath"
	"github.com/google/abfs/shared/remote"
	"github.com/google/abfs/store"
)

type showCmd struct {
	*aclCmd
	*cmd.StoreProvider
}

func NewShowCmd(aclCmd *aclCmd) *showCmd {
	return &showCmd{aclCmd: aclCmd}
}

func (c *showCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "show",
		Usage:   "", //TODO: add usage
		Aliases: []string{},
		Desc:    `Show the ACL hierarchy from the provided path.`,
	}
}

func (c *showCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *showCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	//TODO ensure no args?
	return c.run(ctx)
}

func (c *showCmd) run(ctx context.Context) error {
	if c.path == "" {
		return errors.New("path is empty")
	}

	if c.server == entities.ABFSMetaServer && c.project == entities.ABFSMetaProject {
		c.showMetaEvaluation(ctx)
	} else {
		c.showEvaluation(ctx)
	}

	return nil
}

func (c *showCmd) showMetaEvaluation(ctx context.Context) {
	c.fetchAndPrint(ctx, c.Store, c.project, c.path)
}

func (c *showCmd) showEvaluation(ctx context.Context) {
	projects := []string{c.project}
	if c.project != remote.AllProjects {
		projects = append(projects, remote.AllProjects)
	}

	for _, project := range projects {
		// 1. Exact match if not default acl file (it is accounted for in walk below)
		if !strings.HasSuffix(c.path, remote.DefaultACLFile) {
			c.fetchAndPrint(ctx, c.Store, project, c.path)
		}

		// 2. Hierarchy
		refParts := strings.Split(c.path, "/")
		for i := len(refParts) - 1; i >= 0; i-- {
			parentRef := strings.Join(refParts[0:i], "/")
			parentACLPath := fmt.Sprintf("%s/%s", parentRef, remote.DefaultACLFile)
			if parentRef == "" {
				parentACLPath = remote.DefaultACLFile
			}
			c.fetchAndPrint(ctx, c.Store, project, parentACLPath)
		}
	}

}

func (c *showCmd) fetchAndPrint(ctx context.Context, casStore entities.CASStore, project, path string) {
	hp := &hashpath.HashPath{
		Ref:  remote.ACLRef,
		Path: path,
	}

	res, err := hashpath.Resolve(ctx, casStore, hp, c.server, project)
	if err != nil {
		fmt.Printf("Path: acl:%s (Project: %q) - Not Found\n\n", path, project)
		return
	}

	b, err := store.ReadAllBlob(ctx, casStore, res.Resolved)
	if err != nil {
		fmt.Printf("Path: acl:%s (Project: %q) - Error: %v\n\n", path, project, err)
		return
	}

	fmt.Printf("Path: acl:%s (Project: %q)\n", path, project)
	if strings.HasSuffix(path, remote.DefaultACLFile) {
		var acl remote.PatternedACL
		if err := json.Unmarshal(b, &acl); err != nil {
			log.Printf("failed to unmarshal patterned acl %s: %v", path, err)
			return
		}
		pretty, _ := json.MarshalIndent(acl, "", "  ")
		fmt.Println(string(pretty))
	} else {
		var acl remote.ACL
		if err := json.Unmarshal(b, &acl); err != nil {
			log.Printf("failed to unmarshal acl %s: %v", path, err)
			return
		}
		pretty, _ := json.MarshalIndent(acl, "", "  ")
		fmt.Println(string(pretty))
	}
	fmt.Println()
}
