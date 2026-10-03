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
	"context"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"syscall"
	"time"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/cmd/abfs/nsjail"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/objects"
	"github.com/google/abfs/store"
)

func (c *rerunCmd) evalNoABFS(ctx context.Context) (*binding.EvalResponse, error) {
	srcDir := path.Join(c.rootDir, entities.SrcDir)
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		return nil, fmt.Errorf("cannot create dir %s: %w", srcDir, err)
	}

	if err := os.Chdir(srcDir); err != nil {
		return nil, fmt.Errorf("cannot change current dir to %s: %w", srcDir, err)
	}

	files, err := c.setupNoABFS(ctx)
	if err != nil {
		return nil, err
	}

	resp := &binding.EvalResponse{
		Version:       binding.CurrentEvalResponseVersion,
		EvalRequest:   c.req.Hash,
		MissingReads:  map[string]entities.PatchEntryState{},
		MissingWrites: map[string]entities.PatchEntryState{},
	}

	start := time.Now()
	resp.RunInfo.Timestamp = start
	defer func() {
		resp.RunInfo.RunDuration = time.Since(start).Seconds()
	}()

	nsjailOpts, err := c.nsjailOpts(c.rootDir)
	if err != nil {
		return nil, err
	}

	nsjailOpts = append(nsjailOpts, "-B", srcDir+":/src")

	cmd := "exec 2>&-; exec 2>&1; " + c.command
	log.Println("Running nsjail...")
	out, err := nsjail.RunNSJailCapture(path.Join(c.topDir, c.nsJailBin), path.Join(c.topDir, c.nsJailCfg), nsjailOpts, "/bin/sh", "-c", cmd)

	resp.Stdout = string(out)

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			resp.ExitCode = exitErr.ExitCode()
		} else {
			log.Printf("command failed: %s", err)
			return nil, err
		}
	}

	if err := c.noABFSReadPatches(files, start, resp); err != nil {
		return nil, err
	}
	binding.ProcessResp(binding.EvalPostProcess{
		Resp: resp,
		Req:  c.req,
	})
	return resp, nil
}

func (*rerunCmd) noABFSReadPatches(files map[string]entities.Hash, start time.Time, resp *binding.EvalResponse) error {
	err := filepath.Walk(".", func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.Mode().IsDir() {
			return nil
		}

		if hash, ok := files[path]; ok {
			t := info.Sys().(*syscall.Stat_t).Atim
			if start.After(time.Unix(t.Sec, t.Nsec)) {
				return nil
			}

			mode := entities.FileModeRegular
			if info.Mode()&os.ModeSymlink != 0 {
				mode = entities.FileModeSymlink
			} else if info.Mode()&0111 != 0 {
				mode = entities.FileModeExecutable
			}

			resp.ReadPatch = append(resp.ReadPatch, &entities.PatchEntry{
				Path: path,
				Mode: mode,
				Hash: hash,
				Size: info.Size(),
			})
		}

		if info.ModTime().Before(start) {
			return nil
		}

		p := &entities.PatchEntry{
			Path: path,
			Mode: entities.FileModeRegular,
			Size: info.Size(),
		}

		switch {
		case info.Mode()&os.ModeSymlink != 0:
			p.Mode = entities.FileModeSymlink
			target, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("cannot read symlink %s: %w", path, err)
			}
			p.Hash = objects.HashBlobBytes([]byte(target))
		default:
			p.Mode = entities.FileModeRegular
			if info.Mode()&0111 != 0 {
				p.Mode = entities.FileModeExecutable
			}
			p.Hash, _, err = objects.HashFileBlob(path)
			if err != nil {
				return fmt.Errorf("cannot hash file %s: %w", path, err)
			}
		}

		resp.WritePatch = append(resp.WritePatch, p)
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func (c *rerunCmd) setupNoABFS(ctx context.Context) (map[string]entities.Hash, error) {
	files := map[string]entities.Hash{}

	if c.tmpDir != "" {
		tmpDir, err := filepath.Rel("/src", c.tmpDir)
		if err != nil {
			tmpDir = c.tmpDir
		}
		if err := os.MkdirAll(tmpDir, 0755); err != nil {
			return nil, fmt.Errorf("cannot create dir %s: %w", tmpDir, err)
		}
		files[tmpDir] = entities.ZeroHash
	}

	for _, t := range c.req.Targets {
		if err := os.MkdirAll(path.Dir(t), 0755); err != nil {
			return nil, fmt.Errorf("cannot create dir %s: %w", path.Dir(t), err)
		}
		files[path.Dir(t)] = entities.ZeroHash
	}
	for _, p := range c.resp.ReadPatch {
		if err := os.MkdirAll(path.Dir(p.Path), 0755); err != nil {
			return nil, fmt.Errorf("cannot create dir %s: %w", path.Dir(p.Path), err)
		}
		if err := c.downloadFile(ctx, p); err != nil {
			return nil, fmt.Errorf("cannot download file %s (%s): %w", p.Path, p.Hash, err)
		}
		files[p.Path] = p.Hash
	}

	if err := c.writePathInterposer(path.Join(c.outDir, ".path_interposer_origpath")); err != nil {
		return nil, err
	}

	return files, nil
}

func (c *rerunCmd) downloadFile(ctx context.Context, p *entities.PatchEntry) (retErr error) {
	switch p.Mode {
	case entities.FileModeDir:
		return os.MkdirAll(p.Path, 0755)
	case entities.FileModeSymlink:
		content, err := store.ReadAllBlob(ctx, c.store, p.Hash)
		if err != nil {
			return err
		}
		return os.Symlink(string(content), p.Path)
	case entities.FileModeRegular, entities.FileModeExecutable:
		mode, err := p.Mode.ToOSFileMode()
		if err != nil {
			return err
		}
		file, err := os.OpenFile(p.Path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, mode)
		if err != nil {
			return err
		}
		defer entities.CloseErrCheck(file, &retErr)

		r, err := c.store.OpenBlob(ctx, p.Hash)
		if err != nil {
			return err
		}
		defer entities.CloseErrCheck(r, &retErr)

		_, err = io.Copy(file, r)
		return err
	default:
		return fmt.Errorf("unsupported file mode: %s", p.Mode)
	}
}
