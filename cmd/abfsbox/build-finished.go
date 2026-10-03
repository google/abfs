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
	"fmt"
	"log"
	"os"
	"path"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"go.coder.com/cli"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/ninjago"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cacheman/protocol"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/metrics"
	"github.com/google/abfs/store"
)

type BuildFinishedCmd struct {
	errStr     string
	store      *store.CASStore
	mntDetails *binding.MountDetails
}

func NewBuildFinishedCmd() *BuildFinishedCmd {
	return &BuildFinishedCmd{}
}

func (c *BuildFinishedCmd) Spec() cli.CommandSpec {
	return cli.CommandSpec{
		Name:    "build-finished",
		Usage:   "",
		Aliases: []string{},
		Desc:    `Finalize an ABFS build and push a build object.`,
		Hidden:  true,
	}
}

func (c *BuildFinishedCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVarP(&c.errStr, "error", "e", "", "The error from the build")
}

func (c *BuildFinishedCmd) Run(fl *pflag.FlagSet) {
	if err := c.run(); err != nil {
		log.Fatal(err)
	}
}

func (c *BuildFinishedCmd) run() error {
	info, err := c.newBuildInfo()
	if err != nil {
		return err
	}
	if err = writeBuildInfo(info); err != nil {
		return err
	}

	status := "success"
	if info.Error != "" || c.errStr != "" {
		status = "failure"
	}

	buildGoal := "unknown"
	if len(info.NinjaRunInfo) > 0 && len(info.NinjaRunInfo[len(info.NinjaRunInfo)-1].GoalTargets) > 0 {
		buildGoal = strings.Join(info.NinjaRunInfo[len(info.NinjaRunInfo)-1].GoalTargets, " ")
	} else if len(info.BuildCommand) > 0 {
		buildGoal = strings.Join(info.BuildCommand, " ")
	}

	buildDur := info.End.Sub(info.Start)
	phases := make(map[string]float64)
	hasNinjaRun := false
	var cacheHitRatio float64
	var cacheHitCount uint32
	var cacheMissCount uint32
	// skipMetrics is set to true for successful zero-work builds (e.g., no-op builds with hot cache)
	// to prevent reporting a misleading 0% cache hit rate and polluting telemetry.
	// We still want to report metrics for failed builds even if they did zero work.
	skipMetrics := false

	// There may be multiple ninja runs (e.g., Soong bootstrap then main build)
	// Extract build data from the last Ninja run since it represents the main build execution
	if len(info.NinjaRunInfo) > 0 {
		lastRun := info.NinjaRunInfo[len(info.NinjaRunInfo)-1]

		phases["parse"] = lastRun.ParseSecs
		phases["load_ninja_files"] = lastRun.LoadNinjaFilesSecs
		phases["load_files"] = lastRun.LoadFilesSecs
		phases["build_graph"] = lastRun.BuildGraphSecs
		phases["init_states"] = lastRun.InitStatesSecs
		phases["eval_files"] = lastRun.EvalFilesSecs
		phases["eval_nodes"] = lastRun.EvalNodesSecs
		phases["run"] = lastRun.RunSecs

		if lastRun.Error != "" {
			status = "failure"
		}

		hasNinjaRun = true
		cacheHitRatio = lastRun.CacheHitPercent / 100.0
		cacheHitCount = lastRun.CacheHitCount
		cacheMissCount = lastRun.CacheMissCount

		if lastRun.CacheHitCount+lastRun.CacheMissCount == 0 && status == "success" {
			skipMetrics = true
		}
	}

	mntDetails, _, err := readMountDetails()
	if err != nil {
		return err
	}
	c.mntDetails = mntDetails

	// we want to rely on ninjago values if something exists in both ninjago conf and mount details
	// because ninjago might have some overrides necessary for the build (e.g. cache directory inside NSJail)
	ninjagoConf := ninjago.LoadConfig()
	if !ninjagoConf.LocalOnly {
		store, err := setupABFS(ninjagoConf)
		if err != nil {
			return err
		}
		c.store = store
		defer func() {
			if err := c.store.Close(); err != nil {
				log.Printf("Error closing abfs client: %v", err)
			}
		}()
		if err := c.putBuildInfo(context.Background(), info); err != nil {
			return err
		}
		if !skipMetrics {
			androidTarget := metrics.AndroidTarget()

			metricsReq := &protocol.PushBuildMetricsReq{
				AndroidTarget:     androidTarget,
				BuildGoal:         buildGoal,
				Status:            status,
				BuildDurationSecs: buildDur.Seconds(),
				HasNinjaRun:       hasNinjaRun,
				CacheHitRatio:     cacheHitRatio,
				CacheHitCount:     cacheHitCount,
				CacheMissCount:    cacheMissCount,
				Phases:            phases,
			}
			log.Printf("Forward build metrics to Cacheman\n")
			if err := c.store.CachemanClient().PushBuildMetrics(metricsReq); err != nil {
				log.Printf("Failed to forward build metrics to cacheman: %v\n", err)
			}
		} else {
			log.Printf("Skip forwarding build metrics for zero-work build\n")
		}
	}
	return nil
}

func writeBuildInfo(info *binding.BuildInfo) error {
	f, err := os.OpenFile(path.Join(entities.OutDir, BuildInfoFile), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	err = enc.Encode(struct {
		*binding.BuildInfo
		Hash string
	}{
		BuildInfo: info,
		Hash:      info.Hash.String(),
	})
	errC := f.Close()
	if err == nil && errC != nil {
		err = errC
	}
	return err
}

func (c *BuildFinishedCmd) putBuildInfo(ctx context.Context, info *binding.BuildInfo) error {
	if err := c.store.PutObject(ctx, info.Hash, info.Encoded); err != nil {
		return fmt.Errorf("failed to put BuildInfo with hash %s: %w", info.Hash, err)
	}

	defer log.Printf("Saved BuildInfo for %s with hash %s", info.BuildID, info.Hash)
	ref := fmt.Sprintf("refs/abfs/build/%s/%s", c.mntDetails.MetaRefPrefix, info.BuildID)
	refs := []entities.RefHash{{Ref: ref, Hash: info.Hash}}
	return c.store.PutRefs(c.mntDetails.Config.ManifestServer, entities.ABFSMetaProject, refs)
}

func (c *BuildFinishedCmd) newBuildInfo() (*binding.BuildInfo, error) {
	now := time.Now()

	b, startHash, err := readBuildStart()
	if err != nil {
		return nil, err
	}

	root, err := bindcas.Hash(".")
	if err != nil {
		return nil, err
	}

	ninjaLogHash, err := bindcas.Hash(path.Join(entities.OutDir, NinjaLogFile))
	if err != nil && !entities.IsNotExist(err) {
		return nil, err
	}

	wrapLogHash, err := bindcas.Hash(path.Join(entities.OutDir, WrapLogFile))
	if err != nil && !entities.IsNotExist(err) {
		return nil, err
	}

	cmdMapLogHash, err := bindcas.Hash(path.Join(entities.OutDir, CmdMapLogFile))
	if err != nil && !entities.IsNotExist(err) {
		return nil, err
	}

	mismatchesLogHash, err := bindcas.Hash(path.Join(entities.OutDir, MismatchesLogFile))
	if err != nil && !entities.IsNotExist(err) {
		return nil, err
	}

	ninjaInfo, err := c.readNinjaInfo()
	if err != nil {
		return nil, err
	}
	info := &binding.BuildInfo{
		Version:        binding.CurrentBuildInfoVersion,
		BuildStartData: *b,
		BuildStart:     startHash.String(),
		RootEnd:        root.String(),
		End:            now,
		Error:          c.errStr,
		NinjaLog:       ninjaLogHash.String(),
		WrapLog:        wrapLogHash.String(),
		CmdMapLog:      cmdMapLogHash.String(),
		MismatchesLog:  mismatchesLogHash.String(),
		NinjaRunInfo:   ninjaInfo,
	}

	return info, info.Encode()
}

func (c *BuildFinishedCmd) readNinjaInfo() ([]binding.NinjaRunInfo, error) {
	var result []binding.NinjaRunInfo
	for i := 0; ; i++ {
		stat, err := c.readNinjaStat(i)
		if entities.IsNotExist(err) {
			break
		}
		if err != nil {
			return nil, err
		}
		files := make(map[string]string, len(stat.BuildFiles))
		for _, name := range stat.BuildFiles {
			hash, err := bindcas.Hash(name)
			if err != nil && !entities.IsNotExist(err) {
				return nil, err
			}
			files[name] = hash.String()
		}
		stat.BuildFiles = nil
		result = append(result, binding.NinjaRunInfo{
			NinjaStats:  stat,
			BuildsFiles: files,
		})
	}
	return result, nil
}

func (c *BuildFinishedCmd) readNinjaStat(i int) (*binding.NinjaStats, error) {
	f, err := os.Open(fmt.Sprintf("%s.%d", path.Join(entities.OutDir, NinjaStatsFile), i))
	if err != nil {
		return nil, err
	}
	stat := new(binding.NinjaStats)
	if err := json.NewDecoder(f).Decode(stat); err != nil {
		return nil, err
	}
	return stat, nil
}
