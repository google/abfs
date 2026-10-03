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

package buildmanscheduler

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/go-git/go-git/v5/utils/ioutil"
	"github.com/spf13/pflag"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/buildman/scheduler"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/user"
)

type scheduleCmd struct {
	*buildmanSchedulerCmd
	*cmd.ConfigProvider
	*cmd.StoreProvider
	project      string
	topic        string
	manifestHash string
	buildCommand string
	refPrefix    string
	nsjail       bool
}

func NewScheduleCmd(c *buildmanSchedulerCmd) *scheduleCmd {
	return &scheduleCmd{buildmanSchedulerCmd: c}
}

func (c *scheduleCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "schedule",
		Usage:   "",
		Aliases: []string{},
		Desc:    `Send a single buildrequest for the given pinned manifest hash.`,
	}
}

func (c *scheduleCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVarP(&c.project, "project", "p", "", "The GCP project of the topic (defaults to the remote-project config)")
	fl.StringVarP(&c.topic, "topic", "t", "build-request-local", "The name of the topic")
	fl.StringVarP(&c.manifestHash, "manifest-hash", "m", "", "The hash of the pinned manifest to build (required)")
	fl.StringVarP(&c.buildCommand, "build-command", "b", "source build/envsetup.sh && lunch aosp_cf_x86_64_phone-trunk_staging-userdebug && m droid dist", "The build command to run")
	fl.StringVar(&c.refPrefix, "ref-prefix", "", "The ref prefix to record the build under")
	fl.BoolVar(&c.nsjail, "nsjail", false, "Run the build inside nsjail")
}

func (c *scheduleCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)
	return c.run(ctx)
}

func (c *scheduleCmd) run(ctx context.Context) (returnErr error) {
	project := c.project
	if project == "" {
		project = c.Config.RemoteProject
	}
	if project == "" {
		return fmt.Errorf("--project is required when remote-project is not configured")
	}
	if !entities.IsHash(c.manifestHash) {
		return fmt.Errorf("--manifest-hash must be a valid hash, got %q", c.manifestHash)
	}

	currUser, err := user.CurrentUser()
	if err != nil {
		return err
	}

	hostname, err := os.Hostname()
	if err != nil {
		return err
	}

	now := time.Now()
	req := &binding.BuildRequest{
		Version: binding.CurrentBuildRequestVersion,
		RequestCtx: binding.BuildRequestCtx{
			UserName:         currUser.Username,
			HostName:         hostname,
			RequestTime:      now,
			RequestTimestamp: now.UnixNano(),
		},
		PinnedManifestHash: entities.NewHash(c.manifestHash),
		BuildCommand:       c.buildCommand,
		RefPrefix:          c.refPrefix,
		NSJailBuild: binding.NSJailBuild{
			Enabled: c.nsjail,
		},
	}

	if err := req.Encode(); err != nil {
		return fmt.Errorf("error encoding request %v: %v", req, err)
	}

	if err := c.Store.PutObject(ctx, req.Hash, req.Encoded); err != nil {
		return fmt.Errorf("failed to store request object %q: %v", req.Hash, err)
	}

	p, err := scheduler.NewGCPPusher(ctx, project, c.topic)
	if err != nil {
		return err
	}
	defer ioutil.CheckClose(p, &returnErr)

	if err := p.Push(ctx, req.Hash); err != nil {
		return err
	}
	log.Printf("Sent build request with hash %q", req.Hash)
	return nil
}
