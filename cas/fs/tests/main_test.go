// Copyright 2025 Google LLC
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

package tests

import (
	"flag"
	"fmt"
	"os"
	"path"
	"testing"
)

var rootDir string
var cacheDir string
var scratchDir string
var nonCasDir string
var objectsDir string
var arisDir string
var cli string
var env *Env

func init() {
	flag.StringVar(&rootDir, "root", "", "root directory to run tests in")
	flag.StringVar(&nonCasDir, "nonCasDir", "", "non-casfs scratch directory")
	flag.StringVar(&objectsDir, "objectsDir", "", "where to put git objects from non-cas repo")
	flag.StringVar(&arisDir, "arisDir", "", "where ARIs are written to")
	flag.StringVar(&cli, "cli", "", "path to casfsapi cli")
	flag.StringVar(&cacheDir, "cacheDir", "", "casfs cache directory")
	flag.StringVar(&scratchDir, "scratchDir", "", "casfs cache scratch directory")
}

func TestMain(m *testing.M) {
	flag.Parse()
	if rootDir == "" && nonCasDir == "" && objectsDir == "" {
		// These tests need a mounted casfs and are driven by
		// cas/tests/api_test.sh. Skip them in a plain `go test ./...`.
		fmt.Println("SKIP: no -root/-nonCasDir/-objectsDir given; run cas/tests/api_test.sh to run these tests")
		os.Exit(0)
	}
	if rootDir == "" || nonCasDir == "" || objectsDir == "" {
		panic(fmt.Sprintf("Missing required input <rootDir:%s> <nonCasDir:%s> <objectsDir:%s>", rootDir, nonCasDir, objectsDir))

	}
	if _, err := os.Stat(rootDir); err != nil {
		panic(fmt.Sprintf("Root directory %s does not exist", rootDir))
	}
	if _, err := os.Stat(nonCasDir); err != nil {
		panic(fmt.Sprintf("Non cas directory %s does not exist", nonCasDir))
	}
	if _, err := os.Stat(objectsDir); err != nil {
		panic(fmt.Sprintf("Objects directory %s does not exist", objectsDir))
	}
	if arisDir != "" {
		if _, err := os.Stat(arisDir); err != nil {
			panic(fmt.Sprintf("ARIs directory %s does not exist", arisDir))
		}
	}
	// Check that the scratch dir is found at $cacheDir/scratch/$scratchDir to validate
	// all cas[fs|fuse] operate the same
	expScratch := path.Join(cacheDir, "scratch", scratchDir)
	if _, err := os.Stat(expScratch); err != nil {
		panic(fmt.Sprintf("Scratch directory %s does not exists", expScratch))
	}
	var err error
	env, err = AllTestSetup(rootDir, nonCasDir, cli)
	if err != nil {
		panic(fmt.Sprintf("Unable to test setup: %v", err))
	}
	// Create dir for non cas that can be deleted when done
	env.mountObjDir = objectsDir
	env.mountAriDir = arisDir

	err = gitTestInit(env.nonCasfsTestDir, env.mountObjDir)
	if err != nil {
		panic(fmt.Sprintf("Unable to setup git tests: %v", err))
	}
	// Defer does not run after os.Exit(). This defer is only executed on a
	// fatal or panic exit.
	defer AllTestTearDown(env)

	exitCode := m.Run()

	AllTestTearDown(env)
	os.Exit(exitCode)

}
