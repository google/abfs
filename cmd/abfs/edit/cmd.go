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

package edit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/pflag"
	"gopkg.in/yaml.v3"

	aresgrpc "github.com/google/abfs/ares/grpc"
	"github.com/google/abfs/buildman"
	"github.com/google/abfs/editor"
	"github.com/google/abfs/gitpusher"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/hashpath"
	"github.com/google/abfs/shared/instance"
)

type editCmd struct {
	*root.RootCmd
	*cmd.StoreProvider
	out        io.Writer
	hashPath   *hashpath.HashPath
	addFile    bool
	removeFile bool
	fs         bindcas.MemFS
	stdin      bool
}

func NewCmd(rootCmd *root.RootCmd) *editCmd {
	return &editCmd{
		RootCmd: rootCmd,
		out:     os.Stdout,
	}
}

func (c *editCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "edit",
		Usage:   "<blob-hash-path> [--stdin]",
		Aliases: []string{},
		Desc: `Edit the content specified by its hash-path.
When piping input with --stdin, the command will overwrite the specified file with the piped contents.
Otherwise, it will open an interactive editor.

Examples:
	# Interactive editing
	abfs edit config:production

	# Non-interactive editing with pipe
	yq '.servers[0].enabled = false' servers.yml | abfs edit config:production --stdin
	cat new_config.yml | abfs edit config:global --stdin
	abfs cat config:server/ares | yq '(.servers[] | select(.address == "ares-1:*")).enabled = false' | abfs edit config:server/ares --stdin`,
	}
}

func (c *editCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.addFile, "add", "a", false, "Add a new file")
	fl.BoolVarP(&c.removeFile, "remove", "r", false, "Remove an existing file")
	fl.BoolVarP(&c.stdin, "stdin", "s", false, "Read from stdin")
}

func (c *editCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	hp := args.EnsureSingleArg(fl)

	p, err := hashpath.Parse(hp)
	if err != nil {
		return clierr.CLIErr("invalid hash-path: %v", err)
	}
	c.hashPath = p

	if c.addFile && c.removeFile {
		return clierr.CLIErr("--add-file and --remove-file are mutually exclusive")
	}

	return c.run(ctx)
}

func (c *editCmd) run(ctx context.Context) error {
	rootHP := *c.hashPath
	rootHP.Path = "."
	res, err := hashpath.Resolve(ctx, c.Store, &rootHP, entities.ABFSMetaServer, entities.ABFSMetaProject)
	if err != nil {
		if entities.IsNotExist(err) {
			if !c.hashPath.UsesRef() {
				return err
			}
			if c.addFile && c.hashPath.Path == "." {
				return c.addRef()
			}
			return fmt.Errorf("cannot find reference %q. Use --add and with no path to create the ref pointing to an empty dir", c.hashPath.Ref)
		}
		return fmt.Errorf("error resolving the root directory: %w", err)
	}
	if res.FS == nil {
		return fmt.Errorf("cannot resolve to a file-system")
	}
	c.fs = res.FS

	// Apply the mutation
	switch {
	case c.removeFile:
		if err := c.removeExistingFile(); err != nil {
			return fmt.Errorf("failed to remove existing file %w", err)
		}
	case c.stdin:
		if err := c.applyFromStdin(); err != nil {
			return err
		}
	default:
		if err := c.applyInteractiveEdit(); err != nil {
			return err
		}
	}

	// Get new hash after the mutation
	newRootHash, err := c.fs.Hash()
	if err != nil {
		return err
	}
	if !c.hashPath.UsesRef() {
		fmt.Println("New root hash:", newRootHash)
		return nil
	}

	return c.updateRef(res, newRootHash)
}

func (c *editCmd) addRef() error {
	server := c.hashPath.Server
	if server == "" {
		server = entities.ABFSMetaServer
	}

	project := c.hashPath.Project
	if project == "" {
		project = entities.ABFSMetaProject
	}

	err := c.Store.PutRefs(server, project, []entities.RefHash{
		{
			Ref:  c.hashPath.Ref,
			Hash: entities.EmptyDirHash, // When creating a new ref, we point it to an empty directory.
		},
	})
	if err != nil {
		return err
	}
	fmt.Printf("Created reference %s@%s@%s.\n", c.hashPath.Ref, project, server)
	return nil
}

func (c *editCmd) removeExistingFile() error {
	p := c.hashPath.Path
	if p == "" || p == "." {
		return fmt.Errorf("cannot remove the root directory")
	}
	return c.fs.Remove(p)
}

func (c *editCmd) applyFromStdin() error {
	if c.addFile && c.fs.FileExists(c.hashPath.Path) {
		return fmt.Errorf("file %s exists: %w", c.hashPath.Path, os.ErrExist)
	}

	f, err := os.CreateTemp("", "abfs-stdin-*"+c.fileExtension())
	if err != nil {
		return fmt.Errorf("failed to create tmp file: %w", err)
	}
	tmpPath := f.Name()
	defer func() {
		_ = f.Close()
		_ = os.Remove(tmpPath)
	}()

	if _, err := io.Copy(f, os.Stdin); err != nil {
		return fmt.Errorf("failed to read from stdin: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}

	if err := c.validateFile(tmpPath); err != nil {
		return err
	}

	inFile, err := os.Open(tmpPath)
	if err != nil {
		return err
	}
	defer inFile.Close()
	return c.fs.ImportFile(inFile, c.hashPath.Path)
}

func (c *editCmd) applyInteractiveEdit() error {
	if c.addFile && c.fs.FileExists(c.hashPath.Path) {
		return fmt.Errorf("file %s exists: %w", c.hashPath.Path, os.ErrExist)
	}

	stat, err := os.Stdin.Stat()
	if err == nil && (stat.Mode()&os.ModeCharDevice) == 0 {
		return fmt.Errorf("received input on stdin, but expecting terminal edit by default. Did you intend to use the --stdin flag?")
	}

	stagingFile, err := os.CreateTemp("", "abfs-edit-src-*"+c.fileExtension())
	if err != nil {
		return fmt.Errorf("failed to create staging file: %w", err)
	}
	stagingPath := stagingFile.Name()
	defer func() {
		_ = os.Remove(stagingPath)
	}()

	if !c.addFile {
		if err := c.fs.ExportFile(c.hashPath.Path, stagingFile); err != nil {
			_ = stagingFile.Close()
			return fmt.Errorf("failed to export existing file: %w", err)
		}
	}
	if err := stagingFile.Close(); err != nil {
		return fmt.Errorf("failed to close staging file: %w", err)
	}

	safeName := strings.ReplaceAll(c.hashPath.Path, "/", "_")
	if safeName == "" || safeName == "." {
		safeName = c.hashPath.Ref
	}
	rejectedPath := filepath.Join(os.TempDir(), fmt.Sprintf("abfs-edit-%s.rejected", safeName))
	filetype, specs := c.vimSpecs()

	return editor.EditWithValidation(editor.EditOptions{
		TargetFile:    stagingPath,
		TempPrefix:    "abfs-edit-" + safeName + "-",
		FileExtension: c.fileExtension(),
		RejectedFile:  rejectedPath,
		Filetype:      filetype,
		VimSpecs:      specs,
		Validate: func(editedPath string) error {
			return c.validateFile(editedPath)
		},
		Apply: func(validatedPath string) error {
			f, err := os.Open(validatedPath)
			if err != nil {
				return err
			}
			defer f.Close()
			return c.fs.ImportFile(f, c.hashPath.Path)
		},
	})
}

func (c *editCmd) fileExtension() string {
	ext := filepath.Ext(c.hashPath.Path)
	if ext != "" {
		return ext
	}
	if c.hashPath.Ref == "config" {
		return ".yaml"
	}
	return ".tmp"
}

func (c *editCmd) isYAML() bool {
	ext := strings.ToLower(c.fileExtension())
	return c.hashPath.Ref == "config" || ext == ".yaml" || ext == ".yml"
}

func decodeStrictYAML[T any](data []byte, target *T) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	err := dec.Decode(target)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func (c *editCmd) validateFile(filePath string) error {
	if !c.isYAML() {
		return nil
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}

	var generic any
	if err := decodeStrictYAML(data, &generic); err != nil {
		return fmt.Errorf("invalid YAML: %w", err)
	}

	if c.hashPath.Ref != "config" {
		return nil
	}

	switch {
	case c.hashPath.Path == "clients/configs":
		var cfg instance.ConfigRemoteOverrides
		if err := decodeStrictYAML(data, &cfg); err != nil {
			return fmt.Errorf("invalid client config overrides: %w", err)
		}
		return cfg.Validate()

	case c.hashPath.Path == "server/ares":
		var cfg aresgrpc.ARESServerConfig
		if err := decodeStrictYAML(data, &cfg); err != nil {
			return fmt.Errorf("invalid ARES server config: %w", err)
		}
		return cfg.Validate()

	case c.hashPath.Path == "server/buildman":
		var cfg buildman.BuildmanNodeConfig
		if err := decodeStrictYAML(data, &cfg); err != nil {
			return fmt.Errorf("invalid buildman config: %w", err)
		}
		return cfg.Validate()

	case strings.HasPrefix(c.hashPath.Path, "git-pusher/"):
		var cfg gitpusher.GitPusherPoolConfig
		if err := decodeStrictYAML(data, &cfg); err != nil {
			return fmt.Errorf("invalid git-pusher config: %w", err)
		}
		return cfg.Validate()
	}

	return nil
}

func (c *editCmd) vimSpecs() (string, []editor.VimKeySpec) {
	if !c.isYAML() {
		return "", nil
	}
	var specs []editor.VimKeySpec
	if c.hashPath.Ref == "config" {
		switch {
		case c.hashPath.Path == "clients/configs":
			specs = editor.BuildStructVimSpecs[instance.Config]()
		case c.hashPath.Path == "server/ares":
			specs = append(
				editor.BuildStructVimSpecs[aresgrpc.ARESServerConfig](),
				editor.BuildStructVimSpecs[aresgrpc.ARESServer]()...,
			)
		case c.hashPath.Path == "server/buildman":
			specs = append(
				editor.BuildStructVimSpecs[buildman.BuildmanNodeConfig](),
				editor.BuildStructVimSpecs[buildman.BuildmanNode]()...,
			)
		case strings.HasPrefix(c.hashPath.Path, "git-pusher/"):
			specs = editor.BuildStructVimSpecs[gitpusher.GitPusherPoolConfig]()
		}
	}
	return "yaml", specs
}

func (c *editCmd) updateRef(res *hashpath.Resolution, newRootHash entities.Hash) error {
	segs := strings.SplitN(res.Ref, "@", 3)
	ref, project, server := segs[0], segs[1], segs[2]
	if currentHash, err := c.Store.GetRefs(server, project, []string{ref}); err != nil {
		return fmt.Errorf("could not resolve the ref %s, %w", res.Ref, err)
	} else if currentHash[0] != res.Base {
		return fmt.Errorf("cannot update ref %s to %s since is changed since the initial load (from %s to %s)", res.Ref, newRootHash, currentHash[0], res.Base)
	}
	if err := c.Store.PutRefs(server, project, []entities.RefHash{{Ref: ref, Hash: newRootHash}}); err != nil {
		return err
	}
	fmt.Printf("Updated %s to %s\n", res.Ref, newRootHash)
	return nil
}
