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

package integration

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/google/abfs/shared/instance"
	"github.com/google/abfs/shared/remote/servers/inmemory"

	"github.com/google/abfs/shared/remote/grpc/server"

	"github.com/otiai10/copy"
)

type serveInfo struct {
	stop    func()
	address string
}

var (
	abfsBin *Compile

	cachemanCmd *exec.Cmd

	tapBin *Compile

	// Root test directory where all generated files go.
	testDir string

	// Path to testdata directory.
	testData string

	// A abfs base directory.
	abfsBaseDir string

	// A shared cache directory.
	cacheDir string

	// Path to generated git repositories.
	gitRepos = make(map[string]string)

	// Path to generate gerrit source.
	localGerrit string

	sInfo serveInfo
)

const (
	// Relative path to test directory from mount.
	testGitDir = "test"

	// Relative path to manifest file from test git directory.
	relManPath = "default.xml"

	gitBin = "/usr/bin/git"
)

// TestMain runs all tests using a shared abfs binary and cleans up on
// completion of all tests.
func TestMain(m *testing.M) {
	_ = os.Setenv("ABFS_INSTANCE", "default")
	_ = os.Setenv("CACHEMAN_INSTANCE", "default")

	err := allTestSetup()
	if err != nil {
		_ = allTestTearDown()
		log.Fatal(err)
	}
	exitCode := m.Run()

	_ = allTestTearDown()
	os.Exit(exitCode)
}

func manifestForTestRepo(testRepo string) string {
	return filepath.Join(gitRepos[testRepo], relManPath)
}

func restoreGitRepo(path string) error {
	if err := os.Rename(filepath.Join(path, "git"), filepath.Join(path, ".git")); err != nil {
		return fmt.Errorf("couldn't create .git directory: %v", err)
	}
	return nil
}

// These will be cleaned up by removing the root testDir.
func testdataToGitRepo(path string) (string, error) {
	gitDir, err := os.MkdirTemp(testDir, "")
	if err != nil {
		return "", fmt.Errorf("couldn't create a temp directory: %v", err)
	}
	if err := copy.Copy(path, gitDir); err != nil {
		return "", fmt.Errorf("couldn't copy testdata: %v", err)
	}

	if err := restoreGitRepo(gitDir); err != nil {
		return "", err
	}

	return gitDir, nil
}

func storeGitRepo(cacheDir, repoPath string) error {
	options := []string{
		"--auth-type", "none",
		"--disable-tls",
		"-c", cacheDir,
		"-t", "0",
		"-s", sInfo.address,
		"push",
		"-r", repoPath,
	}
	cmd := exec.Command(abfsBin.Path(), options...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func createAndStoreGitRepos(cacheDir string) error {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return errors.New("couldn't get runtime caller")
	}
	testData = path.Join(path.Dir(thisFile), "testdata")
	entries, err := os.ReadDir(testData)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}

		gitRepo, err := testdataToGitRepo(path.Join(testData, e.Name()))
		if err != nil {
			return err
		}
		gitRepos[e.Name()] = gitRepo
		if err := storeGitRepo(cacheDir, gitRepo); err != nil {
			return err
		}
	}

	return nil
}

func startServer(tmpDir string) (serveInfo, error) {
	var si serveInfo

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", 0))
	if err != nil {
		return si, err
	}
	fserver := inmemory.New()
	s, err := server.NewGRPCServer(&server.GRPCServerOpts{Server: fserver})
	if err != nil {
		return si, fmt.Errorf("failed to create grpc server %v", err)
	}
	si.address = fmt.Sprintf("localhost:%d", lis.Addr().(*net.TCPAddr).Port)
	si.stop = func() {
		s.GracefulStop()
		if err := fserver.Close(); err != nil {
			log.Printf("Failed to close the server: %v\n", err)
		}
	}
	go func() {
		if err := s.Serve(lis); err != nil {
			panic(fmt.Sprintf("Failed to serve the ABFS Server: %v\n", err))
		}
	}()

	return si, nil
}

func startCacheman() error {
	cachemanCmd = exec.Command(abfsBin.Path(), "--auth-type", "none", "--disable-tls", instance.DefaultFlavor.DashedInstancesDir(), abfsBaseDir, "-c", cacheDir, "-t", "0", "-s", sInfo.address, "cacheman", "run", "--debug")
	cachemanCmd.Stdout = os.Stdout
	cachemanCmd.Stderr = os.Stderr
	return cachemanCmd.Start()
}

func maybeStopCacheman() {
	if cachemanCmd == nil || cachemanCmd.Process == nil {
		return
	}

	if err := cachemanCmd.Process.Signal(os.Interrupt); err != nil {
		log.Printf("failed to send interrupt signal to cacheman: %v", err)
	}
	if err := cachemanCmd.Wait(); err != nil {
		log.Printf("failed to wait for cacheman to exit: %v", err)
	}
}

func createLocalGerrit() (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("couldn't get runtime caller")
	}
	testData = path.Join(path.Dir(thisFile), "gerrit_testdata")

	sourceDir := path.Join(testDir, "gerrit")
	if err := copy.Copy(testData, sourceDir); err != nil {
		return "", fmt.Errorf("couldn't copy testdata: %v", err)
	}
	repo1Dir := path.Join(sourceDir, "repo1")
	manifestDir := path.Join(sourceDir, "platform/manifest")
	if err := restoreGitRepo(repo1Dir); err != nil {
		return "", err
	}
	if err := restoreGitRepo(manifestDir); err != nil {
		return "", err
	}

	return sourceDir, nil
}

func waitCacheman() error {
	return exec.Command(abfsBin.Path(), "--auth-type", "none", "--disable-tls", instance.DefaultFlavor.DashedInstancesDir(), abfsBaseDir, "-c", cacheDir, "cacheman", "ping").Run()
}

func allTestSetup() error {
	var err error

	testDir, err = os.MkdirTemp("", "")
	if err != nil {
		return fmt.Errorf("unable to create test temp directory: %v", err)
	}

	abfsBaseDir, err = os.MkdirTemp(testDir, "base")
	if err != nil {
		return fmt.Errorf("unable to create abfs base directory: %v", err)
	}
	if err := os.Setenv("ABFS_BASE_DIR", abfsBaseDir); err != nil {
		return err
	}
	if err := os.Setenv("BASE_DIR", abfsBaseDir); err != nil {
		return err
	}
	if err := os.Mkdir(path.Join(abfsBaseDir, "default"), 0755); err != nil {
		return fmt.Errorf("unable to create default directory in abfs base directory: %v", err)
	}

	cacheDir, err = os.MkdirTemp(testDir, "cache")
	if err != nil {
		return fmt.Errorf("unable to create shared cache directory: %v", err)
	}
	if err := os.Mkdir(path.Join(cacheDir, "tmp"), 0755); err != nil {
		return fmt.Errorf("unable to create tmp directory in cache: %v", err)
	}

	abfsBin = NewCompile("cmd/abfs/abfs.go", testDir)
	if _, err = abfsBin.Compile(); err != nil {
		return fmt.Errorf("unable to compile abfs: %v", err)
	}

	tapBin = NewCompile("cmd/tap/tap.go", testDir)
	if _, err = tapBin.Compile(); err != nil {
		return fmt.Errorf("unable to compile tap: %v", err)
	}

	sInfo, err = startServer(testDir)
	if err != nil {
		return fmt.Errorf("unable to start server: %v", err)
	}
	if err := startCacheman(); err != nil {
		return fmt.Errorf("unable to start cacheman: %v", err)
	}
	if err := waitCacheman(); err != nil {
		return fmt.Errorf("failed to get a ping response from cacheman: %v", err)
	}
	if err := createAndStoreGitRepos(cacheDir); err != nil {
		return fmt.Errorf("unable to create and store git repos: %v", err)
	}

	if localGerrit, err = createLocalGerrit(); err != nil {
		return fmt.Errorf("unable to create and store local gerrit testdata: %v", err)
	}

	return nil
}

// Note that if a test is terminated due to a timeout then
// this method is not ran.
func allTestTearDown() error {
	if sInfo.stop != nil {
		sInfo.stop()
	}
	maybeStopCacheman()
	if err := os.RemoveAll(testDir); err != nil {
		return fmt.Errorf("unable to cleanup test directory: %v", err)
	}

	return nil
}
