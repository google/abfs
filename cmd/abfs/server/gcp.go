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

package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/remote/servers/gcp"
)

const (
	defaultDatabase              = "cas"
	defaultBtInstance            = "objects"
	defaultMaxCachedHeaders      = gcp.DefaultMaxCachedHeaders
	defaultMaxCachedContents     = gcp.DefaultMaxCachedContents
	defaultLinksContentThreshold = gcp.DefaultLinksContentThreshold
	defaultBtConnPool            = gcp.DefaultBtConnPool
)

type gcpCmd struct {
	*serverCmd
	project               string
	instance              string
	database              string
	btInstance            string
	btAppProfile          string
	btConnPool            int
	bucket                string
	maxCachedHeaders      int
	maxCachedContents     int
	linksContentThreshold int64
}

func (c *gcpCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "gcp",
		Usage:   "[-p <port>] --project <project> --instance <instance> --bucket <bucket> [--db <db>] [--bt-instance <instance>]",
		Aliases: []string{},
		Desc:    `Run a GCP-backed ABFS server.`,
	}
}

func (c *gcpCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVar(&c.project, "project", "", "The GCP Project which contains the databases (required)")
	fl.StringVar(&c.instance, "instance", "", "The GCP Spanner Database Instance (required)")
	fl.StringVar(&c.database, "db", defaultDatabase, "The GCP Spanner Database Name")
	fl.StringVar(&c.btInstance, "bt-instance", defaultBtInstance, "The GCP Bigtable Database Instance")
	fl.StringVar(&c.btAppProfile, "bt-app-profile", "", "The GCP Bigtable App Profile ID (empty uses the instance default app profile)")
	fl.IntVar(&c.btConnPool, "bt-conn-pool", defaultBtConnPool, fmt.Sprintf("Number of gRPC connections in the Bigtable client pool. A non-positive value uses %d. Avoid excessively high values (fd/memory pressure).", gcp.DefaultBtConnPool))
	fl.StringVar(&c.bucket, "bucket", "", "The GCS Bucket Name (required)")
	fl.IntVarP(&c.maxCachedHeaders, "headers-cache", "", defaultMaxCachedHeaders, "The maximum number of cached objects' headers")
	fl.IntVarP(&c.maxCachedContents, "contents-cache", "", defaultMaxCachedContents, "The maximum number of cached objects' content")
	fl.Int64Var(&c.linksContentThreshold, "links-content-threshold", defaultLinksContentThreshold, "The threshold in bytes above which to return links content")
}

func (c *gcpCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)
	return c.run(ctx)
}

func (c *gcpCmd) validate() error {
	var missing []string
	for _, f := range []struct{ name, value string }{
		{"--project", c.project},
		{"--instance", c.instance},
		{"--bucket", c.bucket},
	} {
		if f.value == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required flag(s): %s", strings.Join(missing, ", "))
	}
	return nil
}

func (c *gcpCmd) run(ctx context.Context) error {
	if err := c.validate(); err != nil {
		return err
	}
	server, err := gcp.New(ctx, &gcp.NewServerOpts{
		ProjectID:             c.project,
		InstanceID:            c.instance,
		DatabaseID:            c.database,
		BtInstance:            c.btInstance,
		BtAppProfile:          c.btAppProfile,
		BtConnPool:            c.btConnPool,
		BucketName:            c.bucket,
		MaxCachedHeaders:      c.maxCachedHeaders,
		MaxCachedContents:     c.maxCachedContents,
		LinksContentThreshold: c.linksContentThreshold,
	})
	if err != nil {
		return fmt.Errorf("failed to create the ABFS GCP server: %w", err)
	}
	return c.Serve(ctx, gcp.NewFilterServer(server, nil))
}

// StandaloneGCPServerCmd provides a standalone command runner for the GCP server with flat flags.
type StandaloneGCPServerCmd struct {
	serverCmd *serverCmd
	gcpCmd    *gcpCmd
}

func NewStandaloneGCPServerCmd(rootCmd *root.RootCmd) *StandaloneGCPServerCmd {
	sc := &serverCmd{RootCmd: rootCmd}
	gc := &gcpCmd{serverCmd: sc}
	return &StandaloneGCPServerCmd{
		serverCmd: sc,
		gcpCmd:    gc,
	}
}

func (s *StandaloneGCPServerCmd) RegisterFlags(fl *pflag.FlagSet) {
	s.serverCmd.RegisterFlags(fl)
	s.gcpCmd.RegisterFlags(fl)
}

func (s *StandaloneGCPServerCmd) Run(ctx context.Context) error {
	return s.gcpCmd.run(ctx)
}
