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

package rbe

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/pflag"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	rc "github.com/bazelbuild/remote-apis-sdks/go/pkg/client"
	"github.com/bazelbuild/remote-apis-sdks/go/pkg/digest"
	re "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/hashpath"
	"github.com/google/abfs/store"
)

type rbeCmd struct {
	*root.RootCmd
	*cmd.CachemanProvider
	*cmd.ConfigProvider
	*cmd.StoreProvider
	out        io.Writer
	errOut     io.Writer
	objectType string
	recursive  bool
	project    string
	server     string
	rbeClient  *rc.Client
	rbeDirMap  map[string]*re.Directory
}

func NewCmd(rootCmd *root.RootCmd) *rbeCmd {
	return &rbeCmd{
		RootCmd:   rootCmd,
		out:       os.Stdout,
		errOut:    os.Stderr,
		rbeDirMap: make(map[string]*re.Directory),
	}
}

func (c *rbeCmd) Printf(format string, a ...any) {
	_, _ = fmt.Fprintf(c.out, format, a...)
}

func (c *rbeCmd) Println(a ...any) {
	_, _ = fmt.Fprintln(c.out, a...)
}

func (c *rbeCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:  "rbe",
		Usage: "[-t <type>] [-R] <rbe-digest>|<object-hash>",
		Desc:  `Retrieve, unmarshal and print Bazel RBE objects (Action, ActionResult, Directory, Command).`,
	}
}

func (c *rbeCmd) RegisterFlags(fl *pflag.FlagSet) {
	args.OptChoice(fl, &c.objectType, "type", "t", "", []string{"action", "action-result", "directory", "command"}, "The type of the object. If not specified, it will try to auto-detect.")
	fl.BoolVarP(&c.recursive, "recursive", "R", false, "Retrieve and print directories recursively (only applies to directory objects)")
	fl.StringVarP(&c.project, "project", "p", entities.ABFSMetaProject, "The project that the hashpath belongs to")
	fl.StringVarP(&c.server, "server", "s", entities.ABFSMetaServer, "The server that the hashpath belongs to")
}

func (c *rbeCmd) readObject(ctx context.Context, addr string) ([]byte, error) {
	if d, err := digest.NewFromString(addr); err == nil {
		return c.readRBEObject(ctx, d)
	}
	return c.readABFSObject(ctx, addr)
}

func (c *rbeCmd) readRBEObject(ctx context.Context, d digest.Digest) ([]byte, error) {
	if c.Config.RBERemote == "" {
		return nil, fmt.Errorf("RBE CAS is not enabled")
	}

	if c.rbeClient == nil {
		var err error
		c.rbeClient, err = rc.NewClient(ctx, c.Config.RBEInstance, rc.DialParams{
			Service:               c.Config.RBERemote,
			NoSecurity:            false,
			UseApplicationDefault: true,
		}, rc.RPCTimeouts{
			"GetTree":          15 * time.Minute,
			"BatchReadBlobs":   5 * time.Minute,
			"BatchUpdateBlobs": 5 * time.Minute,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to initialize RBE client: %w", err)
		}
	}

	blob, _, err := c.rbeClient.ReadBlob(ctx, d)

	return blob, err
}

func (c *rbeCmd) readABFSObject(ctx context.Context, input string) ([]byte, error) {
	hp, err := hashpath.Parse(input)
	if err != nil {
		return nil, fmt.Errorf("invalid hash-path or digest: %v", err)
	}

	res, err := hashpath.Resolve(ctx, c.Store, hp, c.server, c.project)
	if err != nil {
		return nil, err
	}
	hash := res.Resolved

	data, err := store.ReadAllBlob(ctx, c.Store, hash)
	if err != nil {
		return nil, fmt.Errorf("failed to read blob: %w", err)
	}
	return data, nil
}

func (c *rbeCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	input := args.EnsureSingleArg(fl)
	data, err := c.readObject(ctx, input)
	if err != nil {
		return err
	}

	var msg proto.Message
	var matchedType string

	if c.objectType != "" {
		switch c.objectType {
		case "action":
			msg = &re.Action{}
		case "action-result", "action_result":
			msg = &re.ActionResult{}
		case "directory":
			msg = &re.Directory{}
		case "command":
			msg = &re.Command{}
		default:
			return fmt.Errorf("invalid type: %s. Must be one of: action, action-result, directory, command", c.objectType)
		}
		if err := proto.Unmarshal(data, msg); err != nil {
			return fmt.Errorf("failed to unmarshal as %s: %w", c.objectType, err)
		}
		matchedType = c.objectType
	} else {
		// Auto-detect
		msg, matchedType, err = c.detectAndUnmarshal(data)
		if err != nil {
			return fmt.Errorf("failed to auto-detect and unmarshal: %w", err)
		}
	}

	dir, isDir := msg.(*re.Directory)
	if c.recursive && !isDir {
		return fmt.Errorf("recursive flag can only be used with directory objects")
	}

	if c.objectType == "" {
		_, _ = fmt.Fprintf(c.errOut, "Auto-detected type: %s\n", matchedType)
	}

	if c.recursive && isDir {
		if c.rbeClient != nil {
			d, err := digest.NewFromString(input)
			if err != nil {
				return err
			}

			dirs, err := c.rbeClient.GetDirectoryTree(ctx, d.ToProto())
			if err != nil {
				return err
			}
			for _, dir := range dirs {
				d, err := digest.NewFromMessage(dir)
				if err != nil {
					return err
				}
				c.rbeDirMap[digestToAddr(d)] = dir
			}
		}

		return c.printDirectoryRecursive(ctx, input, ".", dir)
	}

	m := protojson.MarshalOptions{
		Multiline:       true,
		Indent:          "  ",
		EmitUnpopulated: false,
	}
	jsonBytes, err := m.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal to JSON: %w", err)
	}

	c.Println(string(jsonBytes))
	return nil
}

func (c *rbeCmd) detectAndUnmarshal(data []byte) (proto.Message, string, error) {
	type candidate struct {
		name string
		msg  proto.Message
	}

	candidates := []candidate{
		{"directory", &re.Directory{}},
		{"action-result", &re.ActionResult{}},
		{"action", &re.Action{}},
		{"command", &re.Command{}},
	}

	var bestMatch proto.Message
	var bestName string
	minUnknown := -1

	for _, cand := range candidates {
		// We need a fresh message each time
		var msg proto.Message
		switch cand.name {
		case "action":
			msg = &re.Action{}
		case "action-result":
			msg = &re.ActionResult{}
		case "directory":
			msg = &re.Directory{}
		case "command":
			msg = &re.Command{}
		}

		if err := proto.Unmarshal(data, msg); err == nil {
			// Perform validity checks to avoid false positives due to protobuf wire compatibility
			if cand.name == "directory" {
				if dir, ok := msg.(*re.Directory); ok && !isValidDirectory(dir) {
					continue
				}
			}

			unknownBytes := len(msg.ProtoReflect().GetUnknown())
			if unknownBytes == 0 {
				// Perfect match
				return msg, cand.name, nil
			}
			if minUnknown == -1 || unknownBytes < minUnknown {
				minUnknown = unknownBytes
				bestMatch = msg
				bestName = cand.name
			}
		}
	}

	if bestMatch != nil {
		return bestMatch, fmt.Sprintf("%s (guessed with %d unknown bytes)", bestName, minUnknown), nil
	}

	return nil, "", fmt.Errorf("could not unmarshal into any of Action, ActionResult, Directory, Command")
}

func isValidDirectory(dir *re.Directory) bool {
	for _, f := range dir.Files {
		if f.Digest == nil || f.Digest.Hash == "" {
			return false
		}
	}
	for _, d := range dir.Directories {
		if d.Digest == nil || d.Digest.Hash == "" {
			return false
		}
	}
	return true
}

func digestToAddr(dg digest.Digest) string {
	if len(dg.Hash) == entities.HashHexSize {
		return fmt.Sprintf("%s", dg.Hash)
	}
	return fmt.Sprintf("%s/%d", dg.Hash, dg.Size)
}

func (c *rbeCmd) printDirectoryRecursive(ctx context.Context, addr string, currentPath string, dir *re.Directory) error {
	if dir == nil {
		var ok bool
		dir, ok = c.rbeDirMap[addr]
		if !ok {
			data, err := c.readObject(ctx, addr)
			if err != nil {
				return fmt.Errorf("failed to read blob %s: %w", addr, err)
			}
			dir = &re.Directory{}
			if err := proto.Unmarshal(data, dir); err != nil {
				return fmt.Errorf("failed to unmarshal directory %s: %w", addr, err)
			}
		}
	}

	if currentPath == "." {
		c.Printf("%-80s %-10s %-10s %s\n", "HASH", "TYPE", "SIZE", "PATH")
		c.Printf("%-80s %-10s %-10s %s\n", "----", "----", "----", "----")
	}

	getFullPath := func(name string) string {
		if currentPath == "." {
			return name
		}
		return currentPath + "/" + name
	}

	for _, f := range dir.Files {
		sizeStr := "-"
		if f.Digest != nil {
			sizeStr = fmt.Sprintf("%d", f.Digest.SizeBytes)
		}
		dg := "-"
		if f.Digest != nil {
			dg = digestToAddr(digest.NewFromProtoUnvalidated(f.Digest))
		}
		typeStr := "FILE"
		if f.IsExecutable {
			typeStr = "EXEC"
		}
		c.Printf("%-80s %-10s %-10s %s\n", dg, typeStr, sizeStr, getFullPath(f.Name))
	}
	for _, sym := range dir.Symlinks {
		c.Printf("%-80s %-10s %-10s %s -> %s\n", "-", "SYMLINK", "-", getFullPath(sym.Name), sym.Target)
	}
	for _, d := range dir.Directories {
		sizeStr := "-"
		if d.Digest != nil {
			sizeStr = fmt.Sprintf("%d", d.Digest.SizeBytes)
		}
		dg := "-"
		if d.Digest != nil {
			dg = digestToAddr(digest.NewFromProtoUnvalidated(d.Digest))
		}
		c.Printf("%-80s %-10s %-10s %s\n", dg, "DIR", sizeStr, getFullPath(d.Name))

		if d.Digest == nil {
			continue
		}
		subPath := d.Name
		if currentPath != "." {
			subPath = currentPath + "/" + d.Name
		}
		if err := c.printDirectoryRecursive(ctx, dg, subPath, nil); err != nil {
			return err
		}
	}
	return nil
}
