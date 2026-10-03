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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/hashpath"
	"github.com/google/abfs/shared/remote"
)

type updateCmd struct {
	*aclCmd
	*cmd.StoreProvider
	*cmd.CachemanProvider
	addWriter     []string
	addReader     []string
	deleteWriter  []string
	deleteReader  []string
	deleteFile    bool
	pattern       string
	deletePattern bool
}

func NewUpdateCmd(aclCmd *aclCmd) *updateCmd {
	return &updateCmd{aclCmd: aclCmd}
}

func (c *updateCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "update",
		Usage:   "", //TODO: add usage
		Aliases: []string{},
		Desc:    `Update ACL for a given resource`,
	}
}

func (c *updateCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringSliceVar(&c.addWriter, "add-writer", nil, "Entity to add to the writers list")
	fl.StringSliceVar(&c.addReader, "add-reader", nil, "Entity to add to the readers list")
	fl.StringSliceVar(&c.deleteWriter, "delete-writer", nil, "Entity to delete from the writers list")
	fl.StringSliceVar(&c.deleteReader, "delete-reader", nil, "Entity to delete from the readers list")
	fl.BoolVar(&c.deleteFile, "delete-file", false, "When set, the given file will be removed from the ACL dir")
	fl.StringVar(&c.pattern, "pattern", "", "Pattern to update in default.json")
	fl.BoolVar(&c.deletePattern, "delete-pattern", false, "When set, delete the given pattern from default.json")
}

func (c *updateCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	//TODO ensure no args?
	return c.run(ctx)
}

func (c *updateCmd) memfsAndFileForPath(ctx context.Context) (bindcas.MemFS, []byte, error) {
	hp := &hashpath.HashPath{
		Ref:  remote.ACLRef,
		Path: ".",
	}

	buf := new(bytes.Buffer)
	var fs bindcas.MemFS
	res, err := hashpath.Resolve(ctx, c.Store, hp, c.server, c.project)
	if err != nil && !entities.IsNotExist(err) {
		return nil, nil, err
	} else if err == nil {
		// use existing dir for FS
		fs = res.FS
		if fs.FileExists(c.path) {
			if err := res.FS.ExportFile(c.path, buf); err != nil {
				return nil, nil, err
			}
		}
	} else {
		// load empty FS
		fs, err = bindcas.NewMemFS(c.Store, entities.EmptyDirHash)
		if err != nil {
			return nil, nil, err
		}
	}

	// If ref or file doesn't exist, then initialize as an empty serialized object.
	if buf.Len() == 0 {
		buf.WriteString("{}")
	}

	return fs, buf.Bytes(), nil
}

func (c *updateCmd) validate() error {
	if c.path == "" {
		return errors.New("path is empty")
	}

	addOrDeleteReaderWriter := len(c.addWriter) > 0 || len(c.addReader) > 0 || len(c.deleteWriter) > 0 || len(c.deleteReader) > 0
	defaultACLFile := strings.HasSuffix(c.path, remote.DefaultACLFile)

	if c.deleteFile {
		if addOrDeleteReaderWriter {
			return errors.New("cannot specify add-writer, add-reader, delete-writer, or delete-reader when delete-file is set")
		}
		if c.deletePattern {
			return errors.New("cannot specify delete-file and delete-pattern together")
		}
	} else if c.deletePattern {
		if addOrDeleteReaderWriter {
			return errors.New("cannot specify add-writer, add-reader, delete-writer, or delete-reader when delete-pattern is set")
		}

		if c.pattern == "" {
			return errors.New("a pattern must be specified when delete-pattern is set")
		}
	}

	if defaultACLFile && c.pattern == "" && !c.deleteFile {
		return errors.New("a pattern is required for updating the default acl file")
	}

	if !defaultACLFile && c.pattern != "" {
		return errors.New("a pattern must not be specified for updating a specific acl file")
	}

	return nil
}

func (c *updateCmd) run(ctx context.Context) error {
	if err := c.validate(); err != nil {
		return err
	}

	fs, current, err := c.memfsAndFileForPath(ctx)
	if err != nil {
		return fmt.Errorf("failed to create memfs: %w", err)
	}

	var newData []byte
	if c.deleteFile {
		if err := fs.Remove(c.path); err != nil {
			return err
		}
	} else {
		newData, err = c.newACLData(current)
		if err != nil {
			return err
		}

		if bytes.Equal(current, newData) {
			fmt.Println("No changes to apply.")
			return nil
		}

		if err := fs.ImportFile(bytes.NewReader(newData), c.path); err != nil {
			return err
		}
	}

	newRootHash, err := fs.Hash()
	if err != nil {
		return err
	}

	if err := c.Store.PutRefs(c.server, c.project, []entities.RefHash{
		{Ref: remote.ACLRef, Hash: newRootHash},
	}); err != nil {
		return err
	}

	// Wait until cacheman sends the ref.
	fmt.Println("Waiting for cacheman to be idle...")
	if err := c.Cacheman.WaitIdle(1 * time.Minute); err != nil {
		return fmt.Errorf("error waiting for cacheman to be idle: %w", err)
	}

	if c.deleteFile {
		fmt.Printf("Deleted acl:%s\n", c.path)
	} else if c.deletePattern {
		fmt.Printf("Deleted pattern %s from acl:%s\n", c.pattern, c.path)
		fmt.Println(string(newData))
	} else {
		fmt.Printf("Updated acl:%s\n", c.path)
		fmt.Println(string(newData))
	}

	return nil
}

func (c *updateCmd) newACLData(current []byte) ([]byte, error) {
	// Patterned.
	if strings.HasSuffix(c.path, remote.DefaultACLFile) {
		var pacl remote.PatternedACL
		if err := json.Unmarshal(current, &pacl); err != nil {
			return nil, err
		}
		if pacl.Patterns == nil {
			pacl.Patterns = make(map[string]remote.ACL)
		}

		if c.deletePattern {
			if _, ok := pacl.Patterns[c.pattern]; !ok {
				return nil, fmt.Errorf("pattern %q not found", c.pattern)
			}
			delete(pacl.Patterns, c.pattern)
		} else {
			acl := pacl.Patterns[c.pattern]
			if err := c.applyChanges(&acl); err != nil {
				return nil, err
			}
			pacl.Patterns[c.pattern] = acl
		}

		if err := pacl.Validate(); err != nil {
			return nil, err
		}
		updated, err := json.MarshalIndent(pacl, "", "  ")
		if err != nil {
			return nil, err
		}

		return updated, nil
	}

	// Specific.
	var acl remote.ACL
	if err := json.Unmarshal(current, &acl); err != nil {
		return nil, err
	}
	if err := c.applyChanges(&acl); err != nil {
		return nil, err
	}

	updated, err := json.MarshalIndent(acl, "", "  ")
	if err != nil {
		return nil, err
	}

	return updated, nil
}

func (c *updateCmd) applyChanges(acl *remote.ACL) error {
	for _, val := range c.addReader {
		e, err := remote.NewEntity(val)
		if err != nil {
			return err
		}
		acl.AddReaders([]remote.Entity{e})
	}
	for _, val := range c.deleteReader {
		e, err := remote.NewEntity(val)
		if err != nil {
			return err
		}
		acl.RemoveReaders([]remote.Entity{e})
	}
	for _, val := range c.addWriter {
		e, err := remote.NewEntity(val)
		if err != nil {
			return err
		}
		acl.AddWriters([]remote.Entity{e})
	}
	for _, val := range c.deleteWriter {
		e, err := remote.NewEntity(val)
		if err != nil {
			return err
		}
		acl.RemoveWriters([]remote.Entity{e})
	}

	return nil
}
