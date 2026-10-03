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

package rerun

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/binding/run"
	"github.com/google/abfs/cmd/abfs/nsjail"
	"github.com/google/abfs/daemon"
	"github.com/google/abfs/shared/entities"
)

func (c *rerunCmd) evalABFS() (*binding.EvalResponse, error) {
	mntPoint := path.Join(c.rootDir, entities.SrcDir)
	if err := os.MkdirAll(mntPoint, 0755); err != nil {
		return nil, fmt.Errorf("cannot create dir %s: %w", mntPoint, err)
	}

	socket := entities.ABFSSocketFileName
	options := []string{"--fs", string(c.Config.FSType), "casfs-mount", "--socket", socket}
	if c.cpuProf != "" {
		options = append(options, "--cpu-pprof")
		options = append(options, c.cpuProf)
	}
	if c.debug {
		options = append(options, "--debug")
	}
	if c.Config.LocalOnly {
		options = append([]string{"-l"}, options...)
	}
	options = append(options, mntPoint)
	mountCmd := exec.Command(os.Args[0], options...)
	mountCmd.Stdout = os.Stdout
	mountCmd.Stderr = os.Stderr
	if err := mountCmd.Start(); err != nil {
		return nil, fmt.Errorf("abfs mount failed to run '%s': %s", strings.Join(mountCmd.Args, " "), err)
	}
	if !c.keep {
		defer func() {
			if err := mountCmd.Process.Kill(); err != nil {
				log.Printf("Failed to kill the process: %s", err)
			}
			umountCmd := exec.Command(os.Args[0], "umount", mntPoint, "--kill", "--no-mount-details")
			umountCmd.Stdout = os.Stdout
			umountCmd.Stderr = os.Stderr
			if err := umountCmd.Run(); err != nil {
				log.Printf("Failed to run the unmount command: %s", err)
			}

		}()
	}

	if err := daemon.WaitForHash(mntPoint); err != nil {
		return nil, fmt.Errorf("cannot get the expected root hash from the CASFS")
	}

	socketFile := path.Join(mntPoint, socket)
	if err := waitForFile(socketFile, 5*time.Second); err != nil {
		return nil, err
	}
	client, err := binding.NewClient(mntPoint, socket, c.Config.CacheDir, c.Config.LocalOnly, c.Config.FetchTimeout)
	if err != nil {
		return nil, fmt.Errorf("cannot create abfs client: %w", err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			log.Printf("Failed to close the client: %s", err)
		}
	}()

	for _, t := range c.req.Targets {
		if err := os.MkdirAll(path.Join(mntPoint, path.Dir(t)), 0755); err != nil {
			return nil, fmt.Errorf("cannot create dir %s: %w", path.Dir(t), err)
		}
	}

	if err := c.writePathInterposer(path.Join(mntPoint, c.outDir, ".path_interposer_origpath")); err != nil {
		return nil, err
	}

	tmpDir, err := filepath.Rel("/src", c.tmpDir)
	if err != nil {
		tmpDir = c.tmpDir
	}
	if err := os.MkdirAll(path.Join(mntPoint, tmpDir), 0755); err != nil {
		return nil, fmt.Errorf("cannot create dir %s: %s", tmpDir, err)
	}

	if !c.ignorePatch {
		readsToApply := make(entities.Patch, len(c.resp.ReadPatch))
		for i, p := range c.resp.ReadPatch {
			if p.Mode.IsStat() {
				n := *p
				n.Mode.UnsetStat()
				readsToApply[i] = &n
			} else {
				readsToApply[i] = p
			}
		}
		if err := client.ApplyPatch(nil, readsToApply, nil, ""); err != nil {
			return nil, fmt.Errorf("failed to apply the read patch: %w", err)
		}
	}

	nsjailOpts, err := c.nsjailOpts(mntPoint)
	if err != nil {
		return nil, err
	}

	bCmd := run.BuildCommand(c.command)

	log.Println("Running nsjail...")
	if c.noTap {
		err := nsjail.RunNSJail(path.Join(c.topDir, c.nsJailBin),
			path.Join(c.topDir, c.nsJailCfg), nsjailOpts, nil, bCmd.Args...)
		if err != nil {
			log.Printf("command failed: %s", err)
			return nil, err
		}
		return nil, nil
	}

	tapBinPath := "/tap"
	localTap, err := exec.LookPath(tapNoCGo)
	if err != nil {
		return nil, fmt.Errorf("binary %q not found in PATH", tapNoCGo)
	}
	if err := copyFile(localTap, path.Join(c.rootDir, tapBinPath)); err != nil {
		return nil, err
	}

	tapCmd := append([]string{tapBinPath, "--pretty", "--"}, bCmd.Args...)
	stdout, err := nsjail.RunNSJailCapture(path.Join(c.topDir, c.nsJailBin),
		path.Join(c.topDir, c.nsJailCfg), nsjailOpts, tapCmd...)
	if err != nil {
		log.Printf("command failed: %s", err)
		return nil, err
	}

	var tapResult entities.TapCmdResult
	err = json.Unmarshal(stdout, &tapResult)
	if err != nil {
		return nil, err
	}

	if c.withTaps {
		log.Printf("%s", stdout)
	}

	resp := binding.EvalRespFromTapResult(tapResult)
	resp.EvalRequest = c.req.Hash

	oldwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("cannot get current dir: %w", err)
	}
	if err := os.Chdir(mntPoint); err != nil {
		return nil, fmt.Errorf("cannot change current dir to %s: %w", mntPoint, err)
	}
	defer func() {
		if err := os.Chdir(oldwd); err != nil {
			log.Printf("cannot change current dir back to %s: %s", oldwd, err)
		}
	}()

	binding.ProcessResp(binding.EvalPostProcess{
		Resp: resp,
		Req:  c.req,
	})

	return resp, nil
}

func copyFile(source, target string) (retErr error) {
	srcFile, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { retErr = entities.FirstError(retErr, srcFile.Close()) }()

	dstFile, err := os.OpenFile(target, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer func() { retErr = entities.FirstError(retErr, dstFile.Close()) }()

	_, err = io.Copy(dstFile, srcFile)
	return err
}
