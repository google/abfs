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

package inspect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"

	"github.com/spf13/pflag"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/casgit"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/hashpath"
	"github.com/google/abfs/shared/objects/gitobjects"
)

type inspectCmd struct {
	*root.RootCmd
	*cmd.ConfigProvider
	*cmd.StoreProvider
	out            io.Writer
	recursive      bool
	showContent    bool
	showMapResults bool
	showJSON       bool
	showType       bool
	project        string
	server         string
	hash           entities.Hash
}

func NewCmd(rootCmd *root.RootCmd) *inspectCmd {
	return &inspectCmd{RootCmd: rootCmd, out: os.Stdout}
}

func (c *inspectCmd) Printf(format string, a ...any) {
	_, _ = fmt.Fprintf(c.out, format, a...)
}

func (c *inspectCmd) Println(a ...any) {
	_, _ = fmt.Fprintln(c.out, a...)
}

func (c *inspectCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "inspect",
		Usage:   "[-r] [-c] [-m] [-j] [-t] <object-hash>",
		Aliases: []string{"i"},
		Desc:    `Show details of a specified object.`,
	}
}

func (c *inspectCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.recursive, "recursive", "r", false, "Fetch and inspect links recursively")
	fl.BoolVarP(&c.showContent, "content", "c", false, "Show the content of a blob object")
	fl.BoolVarP(&c.showMapResults, "map", "m", false, "Resolve the map and show the results")
	fl.BoolVarP(&c.showJSON, "json", "j", false, "Show in json format")
	fl.BoolVarP(&c.showType, "type", "t", false, "Show the type of the object")
	fl.StringVarP(&c.project, "project", "p", entities.ABFSMetaProject, "The project that the hashpath belongs to")
	fl.StringVarP(&c.server, "server", "s", entities.ABFSMetaServer, "The server that the hashpath belongs to")
}

func (c *inspectCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	hp, err := hashpath.Parse(args.EnsureSingleArg(fl))
	if err != nil {
		return clierr.CLIErr("Invalid hash-path: %v", err)
	}

	// count the number of action options, at most one can be specified
	cc := 0
	for _, f := range []bool{c.showContent, c.showJSON, c.showType} {
		if f {
			cc++
		}
	}
	if cc > 1 {
		return clierr.CLIErr("At most one of these options can be specified (--json, --type or --content)")
	}

	err = c.exec(ctx, hp)
	if entities.IsNotExist(err) {
		return clierr.CLIErr("Object %s cannot be found.", hp)
	}
	return err
}

func (c *inspectCmd) exec(ctx context.Context, hp *hashpath.HashPath) error {
	res, err := hashpath.Resolve(ctx, c.Store, hp, c.server, c.project)
	if err != nil {
		return err
	}
	c.hash = res.Resolved

	if c.showMapResults {
		return c.resolveMap(ctx, c.hash)
	}

	return c.inspect(ctx, c.hash)
}

func (c *inspectCmd) inspect(ctx context.Context, hash entities.Hash) error {
	hdr, err := c.Store.ObjectHeader(ctx, hash)
	if err != nil {
		return err
	}

	if c.showType {
		c.Println(hdr.Type)
		return nil
	}

	if hdr.Type == "blob" {
		return c.inspectBlob(ctx, hash, hdr.Size)
	}

	return c.inspectObject(ctx, hash)
}

var ErrObjectParse = errors.New("object cannot be parsed")

func (c *inspectCmd) resolveMap(ctx context.Context, h entities.Hash) error {
	if !c.Config.LocalOnly {
		if err := c.Store.RefreshMapResults(ctx, h); err != nil {
			return err
		}
	}
	ids, err := c.Store.MapResults(ctx, h)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if !c.recursive {
			c.Println(id)
		} else {
			c.Printf("Result %s:\n", id)
			if err := c.inspect(ctx, id); err != nil {
				return err
			}
			c.Println()
		}
	}
	return nil
}

func (c *inspectCmd) inspectBlob(ctx context.Context, h entities.Hash, size int64) (retErr error) {
	if !c.showContent {
		c.Printf("A blob of size %d\n", size)
		return nil
	}

	file, err := c.Store.OpenBlob(ctx, h)
	if err != nil {
		return err
	}
	defer entities.CloseErrCheck(file, &retErr)

	_, err = io.Copy(c.out, file)
	return err
}

func (c *inspectCmd) inspectObject(ctx context.Context, h entities.Hash) error {
	content, err := c.Store.GetObject(ctx, h)
	if err != nil {
		return err
	}
	t, rest, err := entities.DecodeObject(content)
	if err != nil {
		return err
	}

	if c.showJSON {
		return c.prettyPrintJSON(rest, h, t)
	}

	switch binding.ObjectType(t) {
	case gitobjects.ObjectTypeCommit, gitobjects.ObjectTypeTag:
		c.Printf("%s", string(rest))

	case gitobjects.ObjectTypeDir:
		if err := c.decodeDir(c.hash); err != nil {
			return err
		}

	case gitobjects.ObjectTypeDeepTree:
		deepTree := new(gitobjects.DeepTreeObject)
		if err := deepTree.Decode(h, content); err != nil {
			return err
		}
		c.printDeepTree(deepTree.Entries, 0, "")

	case gitobjects.ObjectTypeSymRef:
		symref := new(gitobjects.SymRefObject)
		if err := symref.Decode(h, content); err != nil {
			return err
		}
		c.Printf("%s", symref.Content)

	case binding.ObjectTypeEnvVars, binding.ObjectTypeMountDetails,
		binding.ObjectTypeBuildStart, binding.ObjectTypeBuildInfo, binding.ObjectTypeUnmountDetails,
		binding.ObjectTypeDeepTreeRequest, binding.ObjectTypeBuildRequest, binding.ObjectTypeBuildResult,
		binding.ObjectTypePackFiles, binding.ObjectTypeLFSRequest, binding.ObjectTypeLFSResult,
		binding.ObjectTypeProjectStatus, binding.ObjectTypeWSSnapshot,
		binding.ObjectTypeDigestMap, binding.ObjectTypeDigestMapRequest:

		if err := c.prettyPrintJSON(rest, h, t); err != nil {
			return err
		}

	case binding.ObjectTypeEvalRequest:
		req := new(binding.EvalRequest)
		if err := req.Decode(h, content); err != nil {
			return err
		}

		c.Printf("Version: %s\n", req.Version)
		c.Printf("Env Vars: %s\n", req.EnvVars)
		c.Printf("Command: %s\n", req.Command)

		c.Println("Targets:")
		for _, t := range req.Targets {
			c.Printf("    %s\n", t)
		}

		c.Println("Inputs:")
		for _, p := range req.InputPatch {
			c.Printf("    %s %s %12d  %s\n", p.Hash, p.Mode, p.Size, p.Path)
		}

	case binding.ObjectTypeEvalResponse:
		resp := new(binding.EvalResponse)
		if err := resp.Decode(h, content); err != nil {
			return err
		}

		if err := resp.Print(c.out); err != nil {
			return err
		}

	case binding.ObjectTypePatch:
		patch := new(binding.PatchObject)
		if err := patch.Decode(h, content); err != nil {
			return err
		}

		for _, p := range patch.Patch {
			c.Printf("%s %s %12d %s\n", p.Hash, p.Mode, p.Size, p.Path)
		}

	case binding.ObjectTypeIndexSnapObject:
		snap := new(binding.IndexSnapObject)
		if err := snap.Decode(h, content); err != nil {
			return err
		}

		for _, ent := range snap.Entries {
			c.Printf("%s %15d %7o %s\n", ent.Hash, ent.Size, ent.Mode, ent.Name)
		}

	default:
		return fmt.Errorf("unknown object type %s", t)
	}

	return nil
}

func (c *inspectCmd) prettyPrintJSON(content []byte, hash entities.Hash, t string) error {
	// Unmarshal and marshal the content to restore encoded characters.
	var blob map[string]any
	if err := json.Unmarshal(content, &blob); err != nil {
		return clierr.CLIErr("Object %s is %s and cannot decode it as JSON: %v", hash, t, err)
	}
	buf := new(bytes.Buffer)
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(blob); err != nil {
		return err
	}
	if _, err := c.out.Write(buf.Bytes()); err != nil {
		return err
	}
	c.Println()
	return nil
}

func (c *inspectCmd) decodeDir(hash entities.Hash) error {
	return casgit.Walk(context.Background(), hash, c.Store, func(p string, mode entities.FileMode, hash entities.Hash) error {
		c.Printf("%s %7o %s\n", hash, mode, p)
		if !c.recursive {
			return casgit.ErrWalkNoStepIn
		}
		return nil
	})
}

func (c *inspectCmd) printDeepTree(entries []*entities.DeepEntry, i int, p string) int {
	e := entries[i]
	c.Printf("%s %7o %13d %s\n", e.Hash, e.Mode, e.Size, path.Join(p, e.Name))
	if e.Mode != entities.FileModeDir {
		return 1
	}
	i++
	for s := int(e.Size - 1); s > 0; {
		n := c.printDeepTree(entries, i, path.Join(p, e.Name))
		s -= n
		i += n
	}
	return int(e.Size)
}
