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
	"bufio"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/spf13/pflag"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/maps"
	"github.com/google/abfs/ninjago"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cacheman/client"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/version"
)

type Hash = entities.Hash

var allowedVars = []string{
	"ANDROID_BUILD_PATHS",
	"ANDROID_BUILD_TOP",
	"ANDROID_EMULATOR_PREBUILTS",
	"ANDROID_HOST_OUT_TESTCASES",
	"ANDROID_HOST_OUT",
	"ANDROID_JAVA_HOME",
	"ANDROID_JAVA_TOOLCHAIN",
	"ANDROID_JAVA11_HOME",
	"ANDROID_JAVA8_HOME",
	"ANDROID_JAVA9_HOME",
	"ANDROID_PRODUCT_OUT",
	"ANDROID_PYTHONPATH",
	"ANDROID_SOONG_HOST_OUT",
	"ANDROID_TARGET_OUT_TESTCASES",
	"ASAN_SYMBOLIZER_PATH",
	"BUILD_DATETIME_FILE",
	"BUILD_ENV_SEQUENCE_NUMBER",
	"BUILD_USERNAME",
	"DISABLE_SOONG_CONFIG_TRACE",
	"DIST",
	"DUMP_MANY_VARS",
	"HAS_BUILD_NUMBER",
	"JAVA_HOME",
	"LANG",
	"LC_MESSAGES",
	"OUT_DIR",
	"OUT",
	"PATH",
	"PLATFORM_VERSION",
	"PWD",
	"PYTHONDONTWRITEBYTECODE",
	"PYTHONPATH",
	"SOONG_GENERATES_NINJA_HINT",
	"TARGET_BOARD_PLATFORM",
	"TARGET_BUILD_APPS",
	"TARGET_BUILD_TYPE",
	"TARGET_BUILD_VARIANT",
	"TARGET_GCC_VERSION",
	"TARGET_PRODUCT",
	"TARGET_RELEASE",
	"TERM_PROGRAM_VERSION",
	"TMPDIR",
	"TOP",
	"UNZIP_DISABLE_ZIPBOMB_DETECTION",
}

// User provided ':' separated list of environment variables to allow
const (
	allowedEnvVar = "ABFS_EXTRA_VARS"
)

type cmd struct {
	logFile     string
	inputPaths  []string
	inputLists  []string
	command     []string
	ninjaConfig *ninjago.Configuration
	cacheman    *client.Client
}

func main() {
	c := &cmd{}
	pflag.StringArrayVar(&c.inputPaths, "input-path", nil, "The file or directory which its hash will be used as an input in the cache request")
	pflag.StringArrayVar(&c.inputLists, "input-list", nil, "The path to a dependency file (each line of the file is a path to a dependency)")
	pflag.StringVar(&c.logFile, "log", entities.OutDir+"/.wrap_log", "The path to log file")
	showVersion := pflag.Bool("version", false, "Print the application version")
	pflag.Parse()

	if *showVersion {
		fmt.Println(version.Version())
		return
	}

	c.command = pflag.Args()

	if len(c.command) == 0 {
		fmt.Fprintf(os.Stderr, "no commands provided\n")
		pflag.Usage()
		os.Exit(1)
	}

	c.ninjaConfig = ninjago.LoadConfig()
	c.cacheman = client.NewClient(c.ninjaConfig.CacheDir, c.ninjaConfig.FetchTimeout)

	rc, err := c.process()
	if err != nil {
		log.Fatal(err)
	}

	os.Exit(rc)
}

func pushFileLines(fileName string, todo chan<- string) (retErr error) {
	file, err := os.Open(fileName)
	if err != nil {
		return err
	}
	defer entities.CloseErrCheck(file, &retErr)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if scanner.Text() != "" {
			todo <- scanner.Text()
		}
	}
	return scanner.Err()
}

func fetchInputs(inputPaths, inputLists []string) (entities.Patch, error) {
	todo := make(chan string)
	var wg sync.WaitGroup
	var lock sync.Mutex
	var err error
	// len(inputPaths) + len(inputLists) is a minimum, not the actual size
	result := make(entities.Patch, 0, len(inputPaths)+len(inputLists))
	for i := 0; i < runtime.NumCPU(); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range todo {
				h, err2 := bindcas.HashStat(p)
				lock.Lock()
				if entities.IsNotExist(err2) {
					// ignore non-existing paths
				} else if err2 != nil {
					err = err2
				} else {
					result = append(result, &entities.PatchEntry{
						Path: p,
						Hash: h.Hash,
						Mode: h.Mode,
						Size: h.Size,
					})
				}
				lock.Unlock()
			}
		}()
	}
	for _, p := range inputPaths {
		todo <- p
	}
	for _, f := range inputLists {
		if err2 := pushFileLines(f, todo); err2 != nil {
			lock.Lock()
			err = err2
			lock.Unlock()
		}
	}
	close(todo)
	wg.Wait()
	if err != nil {
		return nil, err
	}
	return result, nil
}

func allAllowedVars() []string {
	value := strings.TrimSpace(os.Getenv(allowedEnvVar))
	if value == "" {
		return allowedVars
	}

	splits := strings.Split(value, ":")
	for idx := range splits {
		splits[idx] = strings.TrimSpace(splits[idx])
	}

	sz := len(splits) + len(allowedVars)
	all := make([]string, 0, sz)
	all = append(all, allowedVars...)
	all = append(all, splits...)

	return all
}

func filterNonExist(inputPaths, inputLists []string) (filteredPaths []string, filteredLists []string) {
	for _, p := range inputPaths {
		if _, err := os.Stat(p); err == nil {
			filteredPaths = append(filteredPaths, p)
		}
	}
	for _, p := range inputLists {
		if _, err := os.Stat(p); err == nil {
			filteredLists = append(filteredLists, p)
		}
	}

	return
}

func (c *cmd) prefetchInputs(inputs entities.Patch) error {
	if !c.ninjaConfig.PrefetchInputs {
		return nil
	}
	return binding.PrefetchInputPatch(inputs, c.cacheman)
}

func (c *cmd) process() (int, error) {
	allAllowed := allAllowedVars()

	filteredEnv, normalEnv := normalizeEnvVars(os.Environ(), allAllowed)

	cli, err := binding.NewClient(".", entities.ABFSSocketFileName, c.ninjaConfig.CacheDir, c.ninjaConfig.LocalOnly, c.ninjaConfig.FetchTimeout)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err := cli.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to close client: %v\n", err)
		}
	}()

	inputs, err := fetchInputs(filterNonExist(c.inputPaths, c.inputLists))
	if err != nil {
		return 0, err
	}

	envVarsHash, err := cli.StoreEnvVars(normalEnv)
	if err != nil {
		return 0, err
	}
	err = c.setupCtrlDirIfMissing()
	if err != nil {
		return 0, err
	}

	req := &binding.EvalRequest{
		Version:    binding.CurrentEvalRequestVersion,
		Salt:       c.ninjaConfig.EvalSalt,
		Targets:    []string{"'" + strings.Join(c.command, " ") + "'"},
		Command:    strings.Join(c.command, " "),
		InputPatch: inputs,
		EnvVars:    envVarsHash,
	}

	cmd := exec.Command(c.command[0], c.command[1:]...)
	cmd.Env = filteredEnv

	buildStart, err := binding.ReadBuildStart(c.ninjaConfig.Chroot, entities.OutDir)
	if err != nil && !entities.IsNotExist(err) {
		return 0, err
	}

	eval := binding.NewEval(cli, c.ninjaConfig.Chroot, !c.ninjaConfig.StrictTappingOff, req, buildStart)
	eval.Log = os.Stderr
	mapRequest := eval.MapRequest(cmd)
	run := mapRequest.MapFunc
	mapRequest.MapFunc = func(in maps.MapObject) (maps.MapObject, error) {
		if err := c.prefetchInputs(inputs); err != nil {
			log.Printf("failed to prefetch %s: %v", in.GetHash(), err)
		}
		return run(in)
	}
	respObj, err := maps.Map(context.Background(), mapRequest)
	if err != nil {
		return 0, err
	}
	resp := respObj.(*binding.EvalResponse)
	if err := c.log(mapRequest, req, resp); err != nil {
		fmt.Fprintf(os.Stderr, "could not log req=%s resp=%s: %v", req.Hash, resp.Hash, err)
	}
	_, _ = os.Stderr.WriteString(resp.Stderr)
	_, _ = os.Stdout.WriteString(resp.Stdout)
	return resp.ExitCode, nil
}

func (c *cmd) setupCtrlDirIfMissing() error {
	if err := c.setupDir(entities.OutDir); err != nil {
		return err
	}
	if err := bindcas.CreateControl(entities.OutDir); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	return nil
}

func (c *cmd) setupDir(dir string) error {
	if _, err := os.Stat(dir); err != nil {
		log.Printf("Create missing directory %q and exclude from parent", dir)
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	return bindcas.SetExcluded(dir, true)
}

func (c *cmd) log(mapReq *maps.MapRequest, req *binding.EvalRequest, resp *binding.EvalResponse) (retErr error) {
	file, err := os.OpenFile(c.logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer entities.CloseErrCheck(file, &retErr)
	_, err = fmt.Fprintf(file, "%-13d %8.3f %-6t %-40s %-40s %3d %s\n",
		mapReq.StartTime.UnixMilli(),
		mapReq.Duration.Seconds(),
		mapReq.CacheHit,
		req.Hash,
		resp.Hash,
		mapReq.InvalidCount,
		strings.Join(c.command, " "),
	)
	return err
}

var EnvVarReplaces = []struct {
	*regexp.Regexp
	string
}{}

// normalizeEnvVars receives a list of environment variables and a separate list for the variables to keep.
// It will filter out the ones that do not show in the keep-list. It returns the filtered list and a map
// of variables to their normalized values. For normalization it does a set of replacements to unify the output.
func normalizeEnvVars(environ []string, keepVars []string) (filtered []string, normalized map[string]string) {
	set := make(map[string]struct{}, len(keepVars))
	for _, k := range keepVars {
		set[k] = struct{}{}
	}
	filtered = make([]string, 0, len(keepVars))
	normalized = make(map[string]string, len(keepVars))
	cwd, _ := os.Getwd()
	replacer := strings.NewReplacer(cwd, "CWD")
	for _, kv := range environ {
		ss := strings.SplitN(kv, "=", 2)
		if len(ss) < 2 {
			continue
		}
		key, value := ss[0], ss[1]
		if _, keep := set[key]; !keep {
			continue
		}
		filtered = append(filtered, kv)
		value = replacer.Replace(value)
		for _, r := range EnvVarReplaces {
			value = r.ReplaceAllString(value, r.string)
		}
		normalized[key] = value
	}
	return filtered, normalized
}
