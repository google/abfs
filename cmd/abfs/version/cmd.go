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

package version

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/cacheman/protocol"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
	abfsclient "github.com/google/abfs/shared/remote/grpc/client"
	"github.com/google/abfs/shared/version"
)

type versionCmd struct {
	*root.RootCmd
	*cmd.ConfigProvider
	client     bool
	cacheman   bool
	server     bool
	kernel     bool
	all        bool
	needPrefix bool
}

func NewCmd(rootCmd *root.RootCmd) *versionCmd {
	return &versionCmd{RootCmd: rootCmd}
}

func (c *versionCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:  "version",
		Usage: "[--client] [--cacheman] [--server] [--kernel] [--all]",
		Desc:  `Print the application versions.`,
	}
}

func (c *versionCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVar(&c.client, "client", true, "Query the client version.")
	fl.BoolVar(&c.cacheman, "cacheman", false, "Query the cacheman version.")
	fl.BoolVar(&c.server, "server", false, "Query the server version.")
	fl.BoolVar(&c.kernel, "kernel", false, "Query the kernel version.")
	fl.BoolVar(&c.all, "all", false, "Query all versions.")
}

func CachemanVersion(socketPath string) (string, error) {
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return "", fmt.Errorf("could not connect to cacheman service: %v", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			log.Printf("Error closing cacheman connection: %v", err)
		}
	}()

	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return "", fmt.Errorf("could not set connection deadline: %v", err)
	}

	if _, err := conn.Write([]byte{byte(protocol.VersionType)}); err != nil {
		return "", fmt.Errorf("could not write cacheman service: %v", err)
	}

	// 1 byte header
	// 256 byte version
	const (
		maxMessageSize = 257
		tripVal        = maxMessageSize + 1
	)

	var (
		buf           = make([]byte, maxMessageSize)
		bytesRead     = 0
		bytesExpected = tripVal
	)
	for bytesRead < bytesExpected {
		n, err := conn.Read(buf[bytesRead:])
		if err != nil {
			return "", fmt.Errorf("unable to read from cacheman service: %v", err)
		}
		bytesRead += n
		if bytesExpected == tripVal {
			bytesExpected = 1 + int(buf[0])
		}
	}
	verStr := string(buf[1:bytesRead])

	return verStr, nil
}

func (c *versionCmd) numVers() int {
	count := 0
	if c.client || c.all {
		count++
	}

	if c.cacheman || c.all {
		count++
	}

	if c.server || c.all {
		count++
	}

	if c.kernel || c.all {
		count++
	}

	return count
}

func (c *versionCmd) prefix(p string) string {
	if c.needPrefix {
		return p
	}

	return ""
}

func (c *versionCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)
	c.needPrefix = c.numVers() > 1

	fail := false
	if c.client || c.all {
		fmt.Println(c.prefix("client: ") + version.Version())
	}

	if c.cacheman || c.all {
		verStr, err := CachemanVersion(path.Join(c.Config.CacheDir, "socket"))
		if err != nil {
			fail = true
			fmt.Println(c.prefix("cacheman: ") + err.Error())
		} else {
			fmt.Println(c.prefix("cacheman: ") + verStr)
		}
	}

	if c.server || c.all {
		serverVersion := func() {
			servers := c.Config.EffectiveRemoteServers()
			if len(servers) == 0 {
				fail = true
				fmt.Println(c.prefix("server: ") + "remote address is required")

				return
			}
			client, err := abfsclient.NewABFSClient(&abfsclient.ABFSClientOpts{
				Servers:  servers,
				AuthInfo: c.Config.GetAuthInfo(),
			})
			if err != nil {
				fail = true
				fmt.Printf(c.prefix("server: ")+"unable to start abfsclient: %v\n", err)

				return
			}
			timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			ver, err := client.Version(timeoutCtx)
			if err != nil {
				fail = true
				fmt.Println(c.prefix("server: ") + err.Error())

				return
			}
			fmt.Println(c.prefix("server: ") + ver)
		}
		serverVersion()
	}

	if c.kernel || c.all {
		kernel_version := func() {
			verBytes, err := os.ReadFile("/sys/module/casfs/version")
			if err != nil {
				fail = true
				fmt.Println(c.prefix("kernel: ") + "unable to query the CASFS version")

				return
			}
			verStr := strings.TrimSpace(string(verBytes))
			if verStr == "" {
				fail = true
				fmt.Println(c.prefix("kernel: ") + "missing CASFS version")

				return
			}
			fmt.Println(c.prefix("kernel: ") + verStr)
		}
		kernel_version()
	}

	if fail {
		return clierr.CLIErr("unable to detect all specified versions")
	}
	return nil
}
