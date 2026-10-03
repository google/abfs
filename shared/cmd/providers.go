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

package cmd

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/google/abfs/binding"
	cachemanclient "github.com/google/abfs/shared/cacheman/client"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/remote"
	"github.com/google/abfs/shared/remote/grpc/client"
	"github.com/google/abfs/store"
)

type Provider interface {
	Close() error
}

type InstanceResolver interface {
	ResolveInstance() *instance.Instance
}

type ConfigProvider struct {
	Instance *instance.Instance
	Config   *instance.Config
}

func newConfigProvider(c *CliExec) (*ConfigProvider, error) {
	if c.configProvider != nil {
		return c.configProvider, nil
	}
	inst := c.abfsCmd.(InstanceResolver).ResolveInstance()
	c.configProvider = &ConfigProvider{
		Instance: inst,
		Config:   inst.Config,
	}
	c.providers = append(c.providers, c.configProvider)
	return c.configProvider, nil
}

func (p *ConfigProvider) Close() error {
	return nil
}

type StoreProvider struct {
	config *ConfigProvider
	Store  entities.CASStore
}

func newStoreProvider(c *CliExec) (*StoreProvider, error) {
	if c.storeProvider != nil {
		return c.storeProvider, nil
	}
	configProvider, err := newConfigProvider(c)
	if err != nil {
		return nil, err
	}

	var s entities.CASStore
	if !configProvider.Config.NoLocalCache {
		s, err = store.NewCASStore(configProvider.Config.CacheDir, configProvider.Config.LocalOnly, configProvider.Config.FetchTimeout)
		if err != nil {
			return nil, err
		}
	} else {
		cliProvider, err := c.newABFSClientProvider()
		if err != nil {
			return nil, err
		}
		s = remote.NewRemoteStore(cliProvider.ABFSClient)
	}

	c.storeProvider = &StoreProvider{
		config: configProvider,
		Store:  s,
	}
	c.providers = append(c.providers, c.storeProvider)
	return c.storeProvider, nil
}

func (p *StoreProvider) Close() error {
	return p.Store.Close()
}

type CachemanProvider struct {
	config   *ConfigProvider
	Cacheman *cachemanclient.Client
}

func ensureSameCachemanExecutable(inst *instance.Instance) error {
	if os.Getenv("ABFS_SKIP_CACHEMAN_VERIFY") == "1" {
		return nil
	}

	myExe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get current executable: %w", err)
	}
	myStat, err := os.Stat(myExe)
	if err != nil {
		return fmt.Errorf("failed to get current executable stat: %w", err)
	}

	myUid := os.Getuid()
	myPid := os.Getpid()

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return fmt.Errorf("failed to read /proc: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == myPid {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			if int(stat.Uid) != myUid {
				continue
			}
		}

		// Quickly filter by process name (comm)
		commBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
		if err != nil || strings.TrimSpace(string(commBytes)) != "abfs" {
			continue
		}

		cmdlineBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			continue
		}

		// We search for what instance the running cacheman process is using.
		// cmdline is null separated.
		args := strings.Split(string(cmdlineBytes), "\x00")
		hasCacheman := false
		hasRun := false
		hasInstance := false
		for i, arg := range args {
			if arg == "cacheman" {
				hasCacheman = true
			} else if arg == "run" {
				hasRun = true
			} else if arg == "--instance" && i+1 < len(args) && args[i+1] == inst.Name {
				hasInstance = true
			}
		}

		// Verify it is running cacheman for this instance
		if hasCacheman && hasRun && hasInstance {
			cachemanStat, err := os.Stat(fmt.Sprintf("/proc/%d/exe", pid))
			// If file is different, warn the user.
			// 'abfs cacheman stop' and 'abfs init' are recommended because it will re-write the systemd unit file.
			if err == nil && !os.SameFile(myStat, cachemanStat) {
				cachemanExe, _ := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
				log.Printf("Detected running cacheman using different executable or older inode (%s vs %s). This is not a fatal error, but it is encouraged to run 'abfs cacheman stop && abfs cacheman init && abfs cacheman remote-ok' to restart cacheman and wait for connection to the remote before attempting any mounting actions", cachemanExe, myExe)
			}
			break
		}
	}

	return nil
}

func newCachemanProvider(c *CliExec) (*CachemanProvider, error) {
	if c.cachemanProvider != nil {
		return c.cachemanProvider, nil
	}
	configProvider, err := newConfigProvider(c)
	if err != nil {
		return nil, err
	}

	err = ensureSameCachemanExecutable(configProvider.Instance)
	if err != nil {
		return nil, fmt.Errorf("unable to ensure cacheman is up to date: %w", err)
	}

	c.cachemanProvider = &CachemanProvider{
		config:   configProvider,
		Cacheman: cachemanclient.NewClient(configProvider.Config.CacheDir, configProvider.Config.FetchTimeout),
	}

	c.providers = append(c.providers, c.cachemanProvider)
	return c.cachemanProvider, nil
}

func (p *CachemanProvider) Close() error {
	return p.Cacheman.Close()
}

type ABFSClientProvider struct {
	config     *ConfigProvider
	ABFSClient *client.ABFSClient
}

func newABFSClientProvider(c *CliExec) (*ABFSClientProvider, error) {
	if c.abfsClientProvider != nil {
		return c.abfsClientProvider, nil
	}
	configProvider, err := newConfigProvider(c)
	if err != nil {
		return nil, err
	}

	config := configProvider.Config
	servers := config.EffectiveRemoteServers()
	if len(servers) == 0 {
		return nil, errors.New("remote address is required")
	}
	client, err := client.NewABFSClient(&client.ABFSClientOpts{
		Servers:  servers,
		AuthInfo: config.GetAuthInfo(),
	})
	if err != nil {
		return nil, err
	}

	c.abfsClientProvider = &ABFSClientProvider{
		config:     configProvider,
		ABFSClient: client,
	}

	c.providers = append(c.providers, c.abfsClientProvider)
	return c.abfsClientProvider, nil
}

func (p *ABFSClientProvider) Close() error {
	return p.ABFSClient.Close()
}

type MountClientProvider struct {
	config *ConfigProvider
}

func newMountClientProvider(c *CliExec) (*MountClientProvider, error) {
	if c.mountClientProvider != nil {
		return c.mountClientProvider, nil
	}
	configProvider, err := newConfigProvider(c)
	if err != nil {
		return nil, err
	}
	provider := &MountClientProvider{config: configProvider}
	c.mountClientProvider = provider
	c.providers = append(c.providers, provider)
	return provider, nil
}

func (p *MountClientProvider) Client(mountPoint string) (*binding.Client, error) {
	return binding.NewClient(mountPoint, entities.ABFSSocketFileName, p.config.Config.CacheDir, p.config.Config.LocalOnly, p.config.Config.FetchTimeout)
}

func (p *MountClientProvider) Close() error {
	return nil
}
