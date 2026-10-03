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

package ninjago

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/google/abfs/shared/entities"
)

const ConfigFile = ".ninjago.conf"

type Configuration struct {
	Chroot                  string
	CacheDir                string
	LocalOnly               bool
	FetchTimeout            time.Duration
	IgnoreEvalCacheRegexStr string         `json:"IgnoreEvalCacheRegex"`
	IgnoreEvalCacheRegex    *regexp.Regexp `json:"-"`
	LogMismatches           bool
	PSICPU                  float64
	PSIIO                   float64
	PSIMem                  float64
	OrderOnlyAsRegularInput bool
	NoFallbackIfRemoteFails bool
	EvalSalt                string
	EvalNoCache             bool
	NoARES                  bool
	RemoteBootstrap         bool
	RemoteCC                bool
	RemoteJava              bool
	RemoteRust              bool
	RemoteKotlin            bool
	RemoteMetalava          bool
	StrictTappingOff        bool
	PrefetchInputs          bool
	IgnoreSoongEnvAvail     bool
	SkipTapSoongBuild       bool
	ConcurrencyMultiplier   float64
}

var defaultConfig = &Configuration{
	ConcurrencyMultiplier: 0.75,
}

func LoadConfig() *Configuration {
	config, err := ReadConfig(ConfigFile)
	if err != nil {
		if !entities.IsNotExist(err) {
			workingDirectory, _ := os.Getwd()
			fmt.Printf("failed to read the config file '%s' from the working directory '%s'. Using default values. err: %s", ConfigFile, workingDirectory, err)
		}
		return defaultConfig
	}
	if config.ConcurrencyMultiplier == 0 {
		config.ConcurrencyMultiplier = 0.75
	}
	if config.ConcurrencyMultiplier > 1.0 {
		config.ConcurrencyMultiplier = 1.0
	}
	return config
}

func ReadConfig(configFile string) (*Configuration, error) {
	bytes, err := os.ReadFile(configFile)
	if err != nil {
		return nil, err
	}

	config := new(Configuration)
	if err = json.Unmarshal(bytes, config); err != nil {
		configFormat, _ := json.Marshal(*config)
		return nil, fmt.Errorf("failed to unmarshal the contents of config file '%s' (contents: '%s') into the struct '%s': %w", configFile, string(bytes), string(configFormat), err)
	}

	if config.IgnoreEvalCacheRegexStr != "" {
		config.IgnoreEvalCacheRegex, err = regexp.Compile(config.IgnoreEvalCacheRegexStr)
		if err != nil {
			return nil, fmt.Errorf("error compiling the regex '%s': %w", config.IgnoreEvalCacheRegexStr, err)
		}
	}

	return config, nil
}

func (c *Configuration) Write(p string) (retErr error) {
	file, err := os.Create(p)
	if err != nil {
		return err
	}
	defer entities.CloseErrCheck(file, &retErr)

	enc := json.NewEncoder(file)
	enc.SetIndent("", "    ")
	return enc.Encode(c)
}
