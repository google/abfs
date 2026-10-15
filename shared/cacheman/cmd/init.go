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

package cacheman

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/spf13/pflag"

	cachemanclient "github.com/google/abfs/shared/cacheman/client"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/store/localcache"
	"github.com/google/abfs/shared/systemd"
)

type initCmd struct {
	*cachemanCmd
}

func NewInitCmd(c *cachemanCmd) *initCmd {
	return &initCmd{cachemanCmd: c}
}

func (c *initCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:            "init",
		Usage:           "",
		Aliases:         []string{},
		Desc:            "Initialize cache and config",
		ProviderInjects: []any{c.SysCmd.Servicer},
	}
}

func (c *initCmd) RegisterFlags(fl *pflag.FlagSet) {
}

func (c *initCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)

	return c.init(ctx)
}

func (c *initCmd) init(ctx context.Context) error {
	inst := c.Instance
	config := inst.Config
	cfgFile := inst.ConfigFile
	if _, err := os.Stat(cfgFile); err == nil {
		fmt.Printf("Detected existing config file for instance '%s'. Existing keys will be preserved.\n", inst.Name)
	}
	cfgToWrite := c.localConfig()
	if config.WriteToConfig {
		fmt.Printf("Overwriting config for instance '%s' with provided flags...\n", inst.Name)
		cfgToWrite = inst.FlagsConfig.Clone()
	}

	// We don't want to save the 'write-to-config' flag itself into the config file.
	cfgToWrite.WriteToConfig = nil
	inst.LocalConfig = cfgToWrite.Clone()

	fmt.Printf("Writing config for instance '%s' to %s...\n", inst.Name, cfgFile)
	if err := instance.WriteConfig(cfgFile, cfgToWrite, false); err != nil {
		return err
	}

	if err := os.MkdirAll(config.LogDir, 0700); err != nil {
		return fmt.Errorf("failed to create logs directory at %s: %w", config.LogDir, err)
	}

	fmt.Printf("Creating local cache for instance '%s' in %s...\n", inst.Name, config.CacheDir)
	if err := localcache.InitLocalCache(config.CacheDir); err != nil {
		return err
	}
	if config.RBEAsCAS {
		if err := os.MkdirAll(path.Join(config.CacheDir, "identity"), 0700); err != nil {
			return fmt.Errorf("failed to create identity directory at %s: %w", path.Join(config.CacheDir, "identity"), err)
		}
	}
	if config.LocalOnly {
		if err := localcache.WriteCacheVersionIfNotPresent(config.CacheDir); err != nil {
			return err
		}
	}
	// Cacheman is needed in local-only mode too (e.g. mount reads its config
	// from cacheman), so set up the service either way.
	if c.SysCmd.Servicer != nil {
		if err := setupCachemanService(ctx, c.SysCmd.Servicer.Service(), inst); err != nil {
			return err
		}
	}
	fmt.Printf("Initialization for instance '%s' complete.\n", inst.Name)
	return nil
}

// setupCachemanService installs and starts svc and waits for cacheman to
// answer. If systemd can't be used, or another cacheman already serves this
// instance, it prints a warning instead.
func setupCachemanService(ctx context.Context, svc systemd.Service, inst *instance.Instance) error {
	if err := checkSystemctlUser(); err != nil {
		fmt.Fprintf(os.Stderr, systemdUnavailableHelp, svc.Name, err, svc.Command)
		return nil
	}
	if err := checkUnitBaseDir(svc, inst.BaseDir); err != nil {
		return err
	}
	// Each ping gives up after a few seconds, so a wedged cacheman can't hang init.
	ping := cachemanclient.NewHTTPClientWithTimeout(inst.Config.CacheDir, 5*time.Second).Ping
	unitActive := exec.CommandContext(ctx, "systemctl", "--user", "is-active", "--quiet", svc.Name+".service").Run() == nil
	if ping() == nil && !unitActive {
		// Starting the unit now would crash-loop on the cacheman socket lock.
		fmt.Fprintf(os.Stderr, "WARNING: A cacheman that systemd does not manage is already running for "+
			"instance '%s', so %s was not set up. To let systemd manage cacheman, stop that "+
			"cacheman and run init again.\n", inst.Name, svc.Name)
		return nil
	}
	fmt.Printf("Setting up %s service...\n", inst.BinaryName)
	if err := svc.Setup(ctx); err != nil {
		return err
	}
	fmt.Printf("Waiting for %s to respond to ping...\n", svc.Name)
	if err := waitForCacheman(ctx, 2*time.Minute, "ping", ping); err != nil {
		// Don't leave systemd restarting a broken cacheman every second.
		_ = svc.RunAction(context.Background(), "stop")
		return fmt.Errorf("%w\n%s was stopped. Check 'systemctl --user status %s', %s and %s",
			err, svc.Name, svc.Name, svc.StdoutFile, svc.LogFile)
	}
	return nil
}

// checkUnitBaseDir returns an error if svc's existing unit file runs cacheman
// for a different instances base dir. Unit names only include the instance
// name, so overwriting it would repoint another instance's cacheman.
func checkUnitBaseDir(svc systemd.Service, baseDir string) error {
	data, err := os.ReadFile(svc.UnitFile())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	baseDirFlags := []string{instance.ABFSFlavor.DashedInstancesDir(), instance.CachemanFlavor.DashedInstancesDir()}
	for _, line := range strings.Split(string(data), "\n") {
		execStart, ok := strings.CutPrefix(line, "ExecStart=")
		if !ok {
			continue
		}
		args := strings.Fields(execStart)
		for i := 0; i+1 < len(args); i++ {
			if slices.Contains(baseDirFlags, args[i]) && args[i+1] != baseDir {
				return fmt.Errorf("%s already runs cacheman for base dir %s, not %s. "+
					"Use a different --instance name, or remove the existing service first:\n"+
					"  systemctl --user disable --now %s && rm %s",
					svc.Name, args[i+1], baseDir, svc.Name, svc.UnitFile())
			}
		}
	}
	return nil
}

// systemdUnavailableHelp is printed when 'systemctl --user' fails.
// Args: service name, systemctl error, cacheman command.
const systemdUnavailableHelp = `
WARNING: Could not set up the %s systemd user service: 'systemctl --user' failed: %v
Init did not start cacheman, and mounts and builds need it. This usually means
there is no systemd user manager for your user (common over SSH, in containers,
and on GCE VMs with OS Login). To fix it, run:
  sudo loginctl enable-linger "$USER"
  export XDG_RUNTIME_DIR="/run/user/$(id -u)"
On GCE VMs with OS Login, if that fails, let systemd-logind reach the metadata
server, then run the two commands above again:
  sudo mkdir -p /etc/systemd/system/systemd-logind.service.d
  printf '[Service]\nIPAddressAllow=169.254.169.254\nRestrictAddressFamilies=AF_INET\n' |
    sudo tee /etc/systemd/system/systemd-logind.service.d/10-oslogin-metadata.conf
  sudo systemctl daemon-reload && sudo systemctl restart systemd-logind
Then run init again. Or run cacheman yourself, without systemd:
  nohup %s >/dev/null 2>&1 &

`

func (c *initCmd) localConfig() *instance.ConfigOptional {
	config := c.Instance.LocalConfig.Clone()
	config.Apply(c.Instance.FlagsConfig)
	return config
}
