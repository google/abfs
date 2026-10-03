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

package instance

import (
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/user"
)

type Instance struct {
	Name       string
	Dir        string
	BaseDir    string
	Flavor     Flavor
	BinaryName string
	User       string

	// The absolute path to the specific configuration file being used for the current instance.
	ConfigFile           string
	RemoteConfigFile     string
	RemoteConfig         *ConfigOptional
	RemoteConfigChecksum string
	DefaultConfig        *Config
	Config               *Config
	FlagsConfig          *ConfigOptional
	LocalConfig          *ConfigOptional

	configChangeCallbacks []ConfigChangeCallback
	lock                  sync.RWMutex
	watcher               *fsnotify.Watcher
}

type ConfigChangeCallback func(*Config)

func NewInstance(name, dir, baseDir, binaryName string, flagsConfig *ConfigOptional, flavor Flavor) *Instance {
	currentUser, err := user.CurrentUser()
	if err != nil {
		panic(err)
	}
	i := &Instance{
		Name:             name,
		Dir:              dir,
		BaseDir:          baseDir,
		BinaryName:       binaryName,
		User:             currentUser.Username,
		Flavor:           flavor,
		ConfigFile:       filepath.Join(dir, "config"),
		RemoteConfigFile: filepath.Join(dir, ".remote-config"),
		FlagsConfig:      flagsConfig,
		DefaultConfig:    DefaultConfig(dir),
		RemoteConfig:     new(ConfigOptional),
	}
	i.readLocalConfigFile()
	i.readRemoteConfigFile()
	i.setConfig()
	return i
}

func (i *Instance) WriteConfig() error {
	return WriteConfig(i.ConfigFile, i.LocalConfig, false)
}

func (i *Instance) StartWatchingConfigFile() error {
	i.lock.Lock()
	defer i.lock.Unlock()
	if i.watcher != nil {
		return nil
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	i.watcher = watcher
	var setTimer *time.Timer
	debounceDur := 100 * time.Millisecond

	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				switch filepath.Base(event.Name) {
				case filepath.Base(i.ConfigFile):
					if event.Op&fsnotify.Write == fsnotify.Write || event.Op&fsnotify.Create == fsnotify.Create {
						i.readLocalConfigFile()

						if setTimer != nil {
							setTimer.Stop()
						}
						setTimer = time.AfterFunc(debounceDur, func() {
							i.setConfig()
						})
					}
				case filepath.Base(i.RemoteConfigFile):
					if event.Op&fsnotify.Write == fsnotify.Write || event.Op&fsnotify.Create == fsnotify.Create {
						i.readRemoteConfigFile()

						if setTimer != nil {
							setTimer.Stop()
						}
						setTimer = time.AfterFunc(debounceDur, func() {
							i.setConfig()
						})
					}
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Println("error:", err)
			}
		}
	}()

	if err := watcher.Add(path.Dir(i.ConfigFile)); err != nil {
		_ = watcher.Close()
		i.watcher = nil
		return fmt.Errorf("failed to watch config file %q: %w", i.ConfigFile, err)
	}
	return nil
}

func (i *Instance) Close() error {
	i.lock.Lock()
	defer i.lock.Unlock()
	if i.watcher != nil {
		err := i.watcher.Close()
		i.watcher = nil
		return err
	}
	return nil
}

func (i *Instance) readFileConfig(path string) *ConfigOptional {
	config, err := ReadConfig[*ConfigOptional](path)
	if entities.IsNotExist(err) {
		return &ConfigOptional{}
	} else if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read config: %v\n", err)
		return &ConfigOptional{}
	}

	return config
}

func (i *Instance) NewRemoteConfig(remoteConfig *ConfigOptional) {
	i.RemoteConfig = remoteConfig
	i.setConfig()
}

func (i *Instance) RegisterConfigChange(callback ConfigChangeCallback) {
	i.configChangeCallbacks = append(i.configChangeCallbacks, callback)
}

func (i *Instance) notifyConfigChange() {
	for _, callback := range i.configChangeCallbacks {
		callback(i.Config)
	}
}

func (i *Instance) setConfig() {
	config := i.DefaultConfig.Clone()
	config.Apply(i.RemoteConfig)
	config.Apply(i.LocalConfig)
	config.Apply(i.FlagsConfig)

	i.lock.Lock()
	defer i.lock.Unlock()
	i.Config = config
	i.notifyConfigChange()
}

func (i *Instance) readLocalConfigFile() {
	i.LocalConfig = i.readFileConfig(i.ConfigFile)
}

func (i *Instance) readRemoteConfigFile() {
	config := i.readFileConfig(i.RemoteConfigFile)
	checksum := config.Checksum()
	if i.RemoteConfigChecksum == checksum {
		return
	}

	i.RemoteConfig = config
	i.RemoteConfigChecksum = checksum
}

func (i *Instance) SetNewRemoteConfig(remoteConfig *ConfigOptional) {
	checksum := remoteConfig.Checksum()
	if i.RemoteConfigChecksum == checksum {
		return
	}

	if err := WriteConfig(i.RemoteConfigFile, remoteConfig, true); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write remote config: %v\n", err)
		return
	}
	i.RemoteConfig = remoteConfig
	i.RemoteConfigChecksum = checksum
}
