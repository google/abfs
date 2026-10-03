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

package listbuilds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
)

// getDashboardURL returns the build dashboard URL for the given hash, or "" if
// no build-dashboard-url is configured.
func (c *listBuildsCmd) getDashboardURL(hashStr string) string {
	if c.Config == nil || c.Config.BuildDashboardURL == "" {
		return ""
	}
	baseURL := c.Config.BuildDashboardURL
	if !strings.HasSuffix(baseURL, "/") {
		baseURL += "/"
	}
	return baseURL + hashStr
}

type listBuildsCmd struct {
	*root.RootCmd
	*cmd.ConfigProvider
	*cmd.StoreProvider
	*cmd.ABFSClientProvider
	buildStart bool
	verbose    bool
	refPrefix  string
	count      int
	server     string
	allUsers   bool
	jsonOut    bool
}

type BuildInfoRecord struct {
	Hash         string    `json:"hash"`
	DashboardURL string    `json:"dashboard_url,omitempty"`
	Ref          string    `json:"ref"`
	User         string    `json:"user"`
	Start        time.Time `json:"start"`
	Duration     string    `json:"duration"`
	CacheHitPct  []float64 `json:"cache_hit_percentages"`
	Failures     []int     `json:"failures_counts"`
	Targets      []string  `json:"targets"`
}

type BuildStartRecord struct {
	Hash         string    `json:"hash"`
	DashboardURL string    `json:"dashboard_url,omitempty"`
	Ref          string    `json:"ref"`
	User         string    `json:"user"`
	Start        time.Time `json:"start"`
	MountDetails string    `json:"mount_details"`
	RootBegin    string    `json:"root_begin"`
	BuildCommand []string  `json:"build_command"`
}

func NewCmd(rootCmd *root.RootCmd) *listBuildsCmd {
	return &listBuildsCmd{RootCmd: rootCmd}
}

func (c *listBuildsCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "list-builds",
		Usage:   "",
		Aliases: []string{},
		Hidden:  true,
		Desc:    `List builds in chronological order`,
	}
}

func (c *listBuildsCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.buildStart, "build-start", "", false, "Set to search BuildStart refs instead of BuildInfo")
	fl.BoolVarP(&c.verbose, "verbose", "v", true, "Display additional information related to the build (ignored if --json is set)")
	fl.StringVarP(&c.refPrefix, "ref-prefix", "r", "", "Ref prefix to query for builds (default 'user/$USER')")
	fl.IntVarP(&c.count, "count", "c", 10, "Set to limit the results to 'count' number of recent builds")
	fl.StringVarP(&c.server, "server", "s", "", "The server that the refs belong to (default is the manifest-server)")
	fl.BoolVarP(&c.allUsers, "all-users", "a", false, "List builds across all users")
	fl.BoolVarP(&c.jsonOut, "json", "", false, "Output results in JSON format")
}

func (c *listBuildsCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	return c.run(ctx)
}

func (c *listBuildsCmd) run(ctx context.Context) error {
	if c.server == "" {
		c.server = c.Config.ManifestServer
	}

	if c.allUsers && c.refPrefix != "" {
		return errors.New("cannot specify both --all-users and --ref-prefix")
	}

	if c.refPrefix == "" {
		if c.allUsers {
			c.refPrefix = "user/"
		} else {
			c.refPrefix = "user/" + os.Getenv("USER")
		}
	}

	var ref string
	if c.buildStart {
		ref = "refs/abfs/build-start/" + c.refPrefix
	} else {
		ref = "refs/abfs/build/" + c.refPrefix
	}

	refs, err := c.ABFSClient.SearchRefs(ctx, c.server, entities.ABFSMetaProject, ref, 0)
	if err != nil {
		return fmt.Errorf("failed to exec command: %w", err)
	}

	c.sortRefs(refs)

	start := len(refs) - c.count
	if start < 0 {
		start = 0
	}

	slicedRefs := refs[start:]

	if c.jsonOut {
		if c.buildStart {
			records, err := c.getBuildStartRecords(ctx, slicedRefs)
			if err != nil {
				return err
			}
			return c.writeJSON(records)
		} else {
			records, err := c.getBuildInfoRecords(ctx, slicedRefs)
			if err != nil {
				return err
			}
			return c.writeJSON(records)
		}
	} else {
		if c.verbose {
			if err := c.displayVerbose(ctx, slicedRefs); err != nil {
				return err
			}
		} else {
			c.displayConcise(slicedRefs)
		}
	}

	return nil
}

func (c *listBuildsCmd) displayConcise(refHashes []entities.ProjectRefHash) {
	var formatting string
	infoOrStart := "BuildInfo"
	if c.buildStart {
		infoOrStart = "BuildStart"
	}

	if c.allUsers {
		formatting = "%-40s %-15s %s\n"
		fmt.Printf(formatting, infoOrStart, "User", "Ref")
	} else {
		formatting = "%-40s %s\n"
		fmt.Printf(formatting, infoOrStart, "Ref")
	}

	for _, r := range refHashes {
		if c.allUsers {
			user := c.extractUser(r.Ref)
			fmt.Printf(formatting, r.Hash, user, r.Ref)
		} else {
			fmt.Printf(formatting, r.Hash, r.Ref)
		}
	}
}

func (c *listBuildsCmd) displayVerbose(ctx context.Context, refHashes []entities.ProjectRefHash) error {
	if c.buildStart {
		return c.displayBuildStartVerbose(ctx, refHashes)
	} else {
		return c.displayBuildInfoVerbose(ctx, refHashes)
	}
}

func (c *listBuildsCmd) displayBuildInfoVerbose(ctx context.Context, refHashes []entities.ProjectRefHash) error {
	buildInfo := &binding.BuildInfo{}

	var formatting string
	if c.allUsers {
		formatting = "%-40s %-15s %-40s %-15s %-12s %-12s %s\n"
		fmt.Printf(formatting, "Start", "User", "BuildInfo", "CacheHitPct", "Failures", "Duration", "Targets")
	} else {
		formatting = "%-40s %-40s %-15s %-12s %-12s %s\n"
		fmt.Printf(formatting, "Start", "BuildInfo", "CacheHitPct", "Failures", "Duration", "Targets")
	}

	for _, r := range refHashes {
		b, err := c.Store.GetObject(ctx, r.Hash)
		if err != nil {
			return err
		}
		if err := buildInfo.Decode(r.Hash, b); err != nil {
			return err
		}

		var cacheHitPctSB strings.Builder
		var failuresSB strings.Builder
		var targetsSB strings.Builder
		for i, runInfo := range buildInfo.NinjaRunInfo {
			if i > 0 {
				fmt.Fprintf(&cacheHitPctSB, " ")
				fmt.Fprintf(&failuresSB, " ")
				fmt.Fprintf(&targetsSB, " ")
			}
			fmt.Fprintf(&cacheHitPctSB, "%-7.1f", runInfo.CacheHitPercent)
			fmt.Fprintf(&failuresSB, "%-5d", len(runInfo.Failures))
			fmt.Fprintf(&targetsSB, "%v", runInfo.GoalTargets)
		}

		duration := buildInfo.End.Sub(buildInfo.Start)
		duration = duration.Round(1 * time.Second)

		if c.allUsers {
			user := c.getUsername(ctx, buildInfo.MountDetails, r.Ref)
			fmt.Printf(formatting, buildInfo.Start, user, buildInfo.Hash, cacheHitPctSB.String(), failuresSB.String(), duration, targetsSB.String())
		} else {
			fmt.Printf(formatting, buildInfo.Start, buildInfo.Hash, cacheHitPctSB.String(), failuresSB.String(), duration, targetsSB.String())
		}
	}

	return nil
}

func (c *listBuildsCmd) displayBuildStartVerbose(ctx context.Context, refHashes []entities.ProjectRefHash) error {
	buildStart := &binding.BuildStart{}

	var formatting string
	if c.allUsers {
		formatting = "%-40s %-15s %-40s %-40s %-40s %s\n"
		fmt.Printf(formatting, "Start", "User", "BuildStart", "MountDetails", "RootBegin", "BuildCommand")
	} else {
		formatting = "%-40s %-40s %-40s %-40s %s\n"
		fmt.Printf(formatting, "Start", "BuildStart", "MountDetails", "RootBegin", "BuildCommand")
	}

	for _, r := range refHashes {
		b, err := c.Store.GetObject(ctx, r.Hash)
		if err != nil {
			return err
		}
		if err := buildStart.Decode(r.Hash, b); err != nil {
			return err
		}

		if c.allUsers {
			user := c.getUsername(ctx, buildStart.MountDetails, r.Ref)
			fmt.Printf(formatting, buildStart.Start, user, buildStart.Hash, buildStart.MountDetails, buildStart.RootBegin, fmt.Sprintf("%v", buildStart.BuildCommand))
		} else {
			fmt.Printf(formatting, buildStart.Start, buildStart.Hash, buildStart.MountDetails, buildStart.RootBegin, fmt.Sprintf("%v", buildStart.BuildCommand))
		}
	}

	return nil
}

func (c *listBuildsCmd) getBuildInfoRecords(ctx context.Context, refs []entities.ProjectRefHash) ([]BuildInfoRecord, error) {
	records := make([]BuildInfoRecord, len(refs))
	for i, r := range refs {
		b, err := c.Store.GetObject(ctx, r.Hash)
		if err != nil {
			return nil, fmt.Errorf("failed to get object %s: %w", r.Hash, err)
		}
		buildInfo := &binding.BuildInfo{}
		if err := buildInfo.Decode(r.Hash, b); err != nil {
			return nil, fmt.Errorf("failed to decode build info %s: %w", r.Hash, err)
		}

		user := c.getUsername(ctx, buildInfo.MountDetails, r.Ref)

		var cacheHitPcts []float64
		var failuresCounts []int
		var allTargets []string
		for _, runInfo := range buildInfo.NinjaRunInfo {
			cacheHitPcts = append(cacheHitPcts, runInfo.CacheHitPercent)
			failuresCounts = append(failuresCounts, len(runInfo.Failures))
			allTargets = append(allTargets, runInfo.GoalTargets...)
		}

		duration := buildInfo.End.Sub(buildInfo.Start)
		duration = duration.Round(1 * time.Second)

		hashStr := r.Hash.String()
		records[i] = BuildInfoRecord{
			Hash:         hashStr,
			DashboardURL: c.getDashboardURL(hashStr),
			Ref:          r.Ref,
			User:         user,
			Start:        buildInfo.Start,
			Duration:     duration.String(),
			CacheHitPct:  cacheHitPcts,
			Failures:     failuresCounts,
			Targets:      allTargets,
		}
	}
	return records, nil
}

func (c *listBuildsCmd) getBuildStartRecords(ctx context.Context, refs []entities.ProjectRefHash) ([]BuildStartRecord, error) {
	records := make([]BuildStartRecord, len(refs))
	for i, r := range refs {
		b, err := c.Store.GetObject(ctx, r.Hash)
		if err != nil {
			return nil, fmt.Errorf("failed to get object %s: %w", r.Hash, err)
		}
		buildStart := &binding.BuildStart{}
		if err := buildStart.Decode(r.Hash, b); err != nil {
			return nil, fmt.Errorf("failed to decode build start %s: %w", r.Hash, err)
		}

		user := c.getUsername(ctx, buildStart.MountDetails, r.Ref)
		hashStr := r.Hash.String()
		records[i] = BuildStartRecord{
			Hash:         hashStr,
			DashboardURL: c.getDashboardURL(hashStr),
			Ref:          r.Ref,
			User:         user,
			Start:        buildStart.Start,
			MountDetails: buildStart.MountDetails,
			RootBegin:    buildStart.RootBegin,
			BuildCommand: buildStart.BuildCommand,
		}
	}
	return records, nil
}

func (c *listBuildsCmd) writeJSON(data any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(data)
}

func (c *listBuildsCmd) sortRefs(refs []entities.ProjectRefHash) {
	sort.Slice(refs, func(i, j int) bool {
		idI := c.extractBuildID(refs[i].Ref)
		idJ := c.extractBuildID(refs[j].Ref)

		valI, errI := strconv.ParseInt(idI, 10, 64)
		valJ, errJ := strconv.ParseInt(idJ, 10, 64)
		if errI == nil && errJ == nil {
			return valI < valJ
		}
		return idI < idJ
	})
}

func (c *listBuildsCmd) extractBuildID(ref string) string {
	pos := strings.LastIndexByte(ref, '/')
	if pos < 0 {
		return ref
	}
	return ref[pos+1:]
}

func (c *listBuildsCmd) extractUser(ref string) string {
	parts := strings.Split(ref, "/")
	if len(parts) < 5 {
		return "unknown"
	}
	prefixParts := parts[3 : len(parts)-1]
	prefix := strings.Join(prefixParts, "/")

	if prefixParts[0] == "user" && len(prefixParts) > 1 {
		return strings.Join(prefixParts[1:], "/")
	}
	return prefix
}

func (c *listBuildsCmd) getUsername(ctx context.Context, mountDetailsHashStr string, ref string) string {
	if mountDetailsHashStr != "" && c.Store != nil {
		mdHash := entities.NewHash(mountDetailsHashStr)
		mdBytes, err := c.Store.GetObject(ctx, mdHash)
		if err == nil {
			md := &binding.MountDetails{}
			if err := md.Decode(mdHash, mdBytes); err == nil && md.UserName != "" {
				return md.UserName
			}
		}
	}
	return c.extractUser(ref)
}
