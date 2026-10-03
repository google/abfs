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
	"log"
	"syscall"
	"time"

	"go.coder.com/cli"

	aresclient "github.com/google/abfs/ares/grpc/client"
	"github.com/google/abfs/cmd/abfs/acl"
	"github.com/google/abfs/cmd/abfs/ares"
	"github.com/google/abfs/cmd/abfs/build"
	"github.com/google/abfs/cmd/abfs/buildmanbuilder"
	"github.com/google/abfs/cmd/abfs/buildmanscheduler"
	casfsmount "github.com/google/abfs/cmd/abfs/casfs-mount"
	casfsroot "github.com/google/abfs/cmd/abfs/casfs-root"
	"github.com/google/abfs/cmd/abfs/cat"
	"github.com/google/abfs/cmd/abfs/completion"
	"github.com/google/abfs/cmd/abfs/config"
	"github.com/google/abfs/cmd/abfs/deeptree"
	"github.com/google/abfs/cmd/abfs/diags"
	"github.com/google/abfs/cmd/abfs/diff"
	"github.com/google/abfs/cmd/abfs/doctor"
	"github.com/google/abfs/cmd/abfs/du"
	"github.com/google/abfs/cmd/abfs/edit"
	"github.com/google/abfs/cmd/abfs/eval"
	"github.com/google/abfs/cmd/abfs/expiration"
	"github.com/google/abfs/cmd/abfs/export"
	"github.com/google/abfs/cmd/abfs/fetch"
	fileaccess "github.com/google/abfs/cmd/abfs/file-access"
	"github.com/google/abfs/cmd/abfs/git"
	gitclone "github.com/google/abfs/cmd/abfs/git-clone"
	gitindex "github.com/google/abfs/cmd/abfs/git-index"
	gitpusher "github.com/google/abfs/cmd/abfs/git-pusher"
	gitpusherassign "github.com/google/abfs/cmd/abfs/git-pusher-assign"
	"github.com/google/abfs/cmd/abfs/hash"
	"github.com/google/abfs/cmd/abfs/importer"
	initcmd "github.com/google/abfs/cmd/abfs/init"
	"github.com/google/abfs/cmd/abfs/inspect"
	instancecmd "github.com/google/abfs/cmd/abfs/instance"
	"github.com/google/abfs/cmd/abfs/license"
	listbuilds "github.com/google/abfs/cmd/abfs/list-builds"
	"github.com/google/abfs/cmd/abfs/locate"
	"github.com/google/abfs/cmd/abfs/ls"
	"github.com/google/abfs/cmd/abfs/mount"
	mountclient "github.com/google/abfs/cmd/abfs/mount-client"
	"github.com/google/abfs/cmd/abfs/nsjail"
	"github.com/google/abfs/cmd/abfs/purge"
	"github.com/google/abfs/cmd/abfs/push"
	"github.com/google/abfs/cmd/abfs/put"
	"github.com/google/abfs/cmd/abfs/rbe"
	recreatebuild "github.com/google/abfs/cmd/abfs/recreate-build"
	"github.com/google/abfs/cmd/abfs/remount"
	"github.com/google/abfs/cmd/abfs/repo"
	reposetup "github.com/google/abfs/cmd/abfs/repo-setup"
	"github.com/google/abfs/cmd/abfs/rerun"
	"github.com/google/abfs/cmd/abfs/server"
	"github.com/google/abfs/cmd/abfs/setup"
	"github.com/google/abfs/cmd/abfs/sysreport"
	"github.com/google/abfs/cmd/abfs/tar"
	testclient "github.com/google/abfs/cmd/abfs/test-client"
	testcreds "github.com/google/abfs/cmd/abfs/test-creds"
	"github.com/google/abfs/cmd/abfs/unmount"
	versioncmd "github.com/google/abfs/cmd/abfs/version"
	"github.com/google/abfs/cmd/abfs/ws"
	cacheman "github.com/google/abfs/shared/cacheman/cmd"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/remote"
	"github.com/google/abfs/shared/version"
)

func expired() bool {
	if !version.HasExpiration() {
		return false
	}

	exp, err := version.Expiration()
	if err != nil {
		log.Printf("Expiration check error : %v", err)
		return true
	}

	return exp.Compare(time.Now()) == -1
}

// We skip health-check with cacheman so that it can start with an unhealthy connection.
func NewARESClient(address string, authInfo instance.AuthInfo) (remote.ARESService, error) {
	return aresclient.NewARESClientWithOptions(&aresclient.ARESClientOpts{
		Address:         address,
		AuthInfo:        authInfo,
		SkipHealthCheck: true,
	})
}

func populateRootABFSCommands() *root.RootCmd {
	r := root.NewRootCommand()

	r.Commands = []cmd.ABFSCmd{
		initcmd.NewCmd(r),
		build.NewCmd(r),
		mount.NewCmd(r),
		mountclient.NewCmd(r),
		remount.NewCmd(r),
		unmount.NewCmd(r),
		setup.NewCmd(r),
		nsjail.NewCmd(r),
		cacheman.NewCmd(r, NewARESClient),
		inspect.NewCmd(r),
		rbe.NewCmd(r),
		hash.NewCmd(r),
		cat.NewCmd(r),
		ls.NewCmd(r),
		edit.NewCmd(r),
		du.NewCmd(r),
		diff.NewCmd(r),
		diff.NewBuildCmd(r),
		export.NewCmd(r),
		recreatebuild.NewCmd(r),
		rerun.NewCmd(r),
		eval.NewCmd(r),
		casfsmount.NewCmd(r),
		tar.NewCmd(r),
		config.NewCmd(r),
		push.NewCmd(r),
		testclient.NewCmd(r),
		acl.NewCmd(r),
		diags.NewCmd(r),
		versioncmd.NewCmd(r),
		doctor.NewCmd(r),
		gitindex.NewCmd(r),
		fetch.NewCmd(r),
		fileaccess.NewCmd(r),
		purge.NewCmd(r),
		deeptree.NewCmd(r),
		buildmanbuilder.NewCmd(r),
		buildmanscheduler.NewCmd(r),
		git.NewCmd(r),
		repo.NewCmd(r),
		put.NewCmd(r),
		gitclone.NewCmd(r),
		completion.NewCmd(r),
		license.NewCmd(r),
		expiration.NewCmd(r),
		casfsroot.NewCmd(r),
		locate.NewCmd(r),
		ares.NewCmd(r),
		listbuilds.NewCmd(r),
		instancecmd.NewCmd(r),
		sysreport.NewCmd(r),
		testcreds.NewCmd(r),
		gitpusherassign.NewCmd(r),
		importer.NewCmd(r),
		reposetup.NewCmd(r),
		ws.NewCmd(r),
	}

	f := instance.DefaultFlavor.Name
	if f == "abfs" || f == "abfs.server" {
		r.Commands = append(r.Commands, server.NewCmd(r))
	}
	if f == "abfs" || f == "abfs.pusher" {
		r.Commands = append(r.Commands, gitpusher.NewCmd(r))
	}

	return r
}

func main() {
	syscall.Umask(0022)

	if expired() {
		log.Fatal("The binary time allotment has elapsed")
	}

	r := populateRootABFSCommands()

	clis := cmd.ABFSCmdToCli(r)

	cli.RunRoot(clis)
}
