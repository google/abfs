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

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/spf13/pflag"
	"go.coder.com/cli"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/ninjago"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/store"
)

const (
	BuildStartFile    = ".build_start"
	BuildInfoFile     = ".build_info"
	NinjaLogFile      = ".ninja_log"
	WrapLogFile       = ".wrap_log"
	CmdMapLogFile     = ".cmd_map_log"
	MismatchesLogFile = entities.MismatchesLogFile
	NinjaStatsFile    = ".ninja_stats"
)

type BuildStartedCmd struct {
	store      *store.CASStore
	buildArgs  []string
	mntDetails *binding.MountDetails
}

func NewBuildStartedCmd() *BuildStartedCmd {
	return &BuildStartedCmd{}
}

func (c *BuildStartedCmd) Spec() cli.CommandSpec {
	return cli.CommandSpec{
		Name:    "build-started",
		Usage:   "",
		Aliases: []string{},
		Desc:    `Capture the initial state for an ABFS build.`,
		Hidden:  true,
	}
}

func (c *BuildStartedCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *BuildStartedCmd) Run(fl *pflag.FlagSet) {
	c.buildArgs = fl.Args()

	if err := c.run(); err != nil {
		log.Fatal(err)
	}
}

func (c *BuildStartedCmd) run() error {
	err := c.setupDirs()
	if err != nil {
		return err
	}
	lastBuild, err := c.handleLastBuild()
	if err != nil {
		return err
	}
	b, err := c.newBuildStart(lastBuild)
	if err != nil {
		return err
	}
	if err := writeBuildStart(b); err != nil {
		return err
	}

	ninjagoConf := ninjago.LoadConfig()
	if !ninjagoConf.LocalOnly {
		store, err := setupABFS(ninjagoConf)
		if err != nil {
			return err
		}
		c.store = store
		if err := c.putBuildStart(context.Background(), b); err != nil {
			return err
		}
	}

	return nil
}

func setupABFS(ninjagoConf *ninjago.Configuration) (*store.CASStore, error) {
	return store.NewCASStore(ninjagoConf.CacheDir, ninjagoConf.LocalOnly, ninjagoConf.FetchTimeout)
}

func readMountDetails() (result *binding.MountDetails, hash entities.Hash, retErr error) {
	m := struct {
		*binding.MountDetails
		Hash string
	}{}
	f, err := os.Open(".repo/mount-details")
	if err != nil {
		return nil, entities.Hash{}, err
	}
	defer entities.CloseErrCheck(f, &retErr)

	if err := json.NewDecoder(f).Decode(&m); err != nil {
		return nil, entities.Hash{}, err
	}
	if !entities.IsHash(m.Hash) {
		return nil, entities.Hash{}, fmt.Errorf("mount-details is not a hash: %q", m.Hash)
	}
	return m.MountDetails, entities.NewHash(m.Hash), nil
}

func (c *BuildStartedCmd) putBuildStart(ctx context.Context, b *binding.BuildStart) error {
	if err := c.store.PutObject(ctx, b.Hash, b.Encoded); err != nil {
		return fmt.Errorf("failed to put BuildStart with hash %s: %w", b.Hash, err)
	}

	defer log.Printf("Saved BuildStart for %s with hash %s", b.BuildID, b.Hash)
	ref := fmt.Sprintf("refs/abfs/build-start/%s/%s", c.mntDetails.MetaRefPrefix, b.BuildID)
	refs := []entities.RefHash{{Ref: ref, Hash: b.Hash}}
	return c.store.PutRefs(c.mntDetails.Config.ManifestServer, entities.ABFSMetaProject, refs)
}

func readBuildStart() (result *binding.BuildStartData, hash entities.Hash, retErr error) {
	f, err := os.Open(path.Join(entities.OutDir, BuildStartFile))
	if err != nil {
		return nil, entities.Hash{}, err
	}
	defer entities.CloseErrCheck(f, &retErr)

	b := new(struct {
		*binding.BuildInfo
		Hash string
	})
	if err := json.NewDecoder(f).Decode(b); err != nil {
		return nil, entities.Hash{}, err
	}

	return &b.BuildStartData, entities.NewHash(b.Hash), nil
}

func writeBuildStart(b *binding.BuildStart) error {
	f, err := os.OpenFile(path.Join(entities.OutDir, BuildStartFile), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	err = enc.Encode(struct {
		*binding.BuildStart
		Hash string
	}{
		BuildStart: b,
		Hash:       b.Hash.String(),
	})

	errC := f.Close()
	if err == nil && errC != nil {
		err = errC
	}
	return err
}

func readBuildInfo() (buildInfo *binding.BuildInfo, hash entities.Hash, retErr error) {
	m := struct {
		*binding.BuildInfo
		Hash string
	}{}
	f, err := os.Open(path.Join(entities.OutDir, BuildInfoFile))
	if err != nil {
		return nil, entities.Hash{}, err
	}
	defer entities.CloseErrCheck(f, &retErr)

	if err := json.NewDecoder(f).Decode(&m); err != nil {
		return nil, entities.Hash{}, err
	}
	if !entities.IsHash(m.Hash) {
		return nil, entities.Hash{}, fmt.Errorf("build-info is not a hash: %q", m.Hash)
	}
	return m.BuildInfo, entities.NewHash(m.Hash), nil
}

func (c *BuildStartedCmd) newBuildStart(lastBuild string) (*binding.BuildStart, error) {
	h, err := bindcas.Hash(".")
	if err != nil {
		return nil, err
	}

	mntDetails, hash, err := readMountDetails()
	if err != nil {
		return nil, err
	}
	c.mntDetails = mntDetails

	now := time.Now()
	b := &binding.BuildStart{
		Version: binding.CurrentBuildInfoVersion,
		BuildStartData: binding.BuildStartData{
			BuildID:       fmt.Sprintf("%d", now.UnixNano()),
			MountDetails:  hash.String(),
			RootBegin:     h.String(),
			Start:         now,
			PreviousBuild: lastBuild,
			BuildCommand:  c.buildArgs,
			OutDir:        entities.OutDir,
		},
	}
	return b, b.Encode()
}

func (c *BuildStartedCmd) handleBuildInfoMissing() (string, error) {
	b, startHash, err := readBuildStart()
	if entities.IsNotExist(err) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	log.Printf("build-finish is missing for %s (%s)", b.BuildID, startHash)
	cmd := &BuildFinishedCmd{
		errStr: "build-finish is missing",
	}
	if err := cmd.run(); err != nil {
		return "", err
	}
	return c.handleLastBuild()
}

func (c *BuildStartedCmd) handleLastBuild() (string, error) {
	// TODO: Add incremental build support to build-info
	_, hash, err := readBuildInfo()
	if entities.IsNotExist(err) {
		return c.handleBuildInfoMissing()
	} else if err != nil {
		return "", err
	}
	cleanup(entities.OutDir)
	_ = os.Remove(path.Join(entities.OutDir, BuildStartFile))
	_ = os.Remove(path.Join(entities.OutDir, BuildInfoFile))
	return hash.String(), nil
}

func cleanup(outdir string) {
	// Ignore the potential errors
	_ = os.Remove(path.Join(outdir, WrapLogFile))
	_ = os.Remove(path.Join(outdir, CmdMapLogFile))
	_ = os.Remove(path.Join(outdir, MismatchesLogFile))
	matches, _ := filepath.Glob(path.Join(outdir, NinjaStatsFile) + ".*")
	for _, m := range matches {
		_ = os.Remove(m)
	}
}

func (c *BuildStartedCmd) setupDirs() error {
	if err := c.setupOutDir(); err != nil {
		return err
	}
	if err := c.setupDistDir(); err != nil {
		return err
	}
	return nil
}

func (c *BuildStartedCmd) setupOutDir() error {
	if err := c.setupDir(entities.OutDir); err != nil {
		return err
	}
	if err := bindcas.CreateControl(entities.OutDir); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	return nil
}

func (c *BuildStartedCmd) setupDistDir() error {
	if err := c.setupDir(entities.DistDir); err != nil {
		return err
	}
	return nil
}

func (c *BuildStartedCmd) setupDir(dir string) error {
	if _, err := os.Stat(dir); err != nil {
		log.Printf("Create missing directory %q and exclude from parent", dir)
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	return bindcas.SetExcluded(dir, true)
}
