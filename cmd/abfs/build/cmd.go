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

package build

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"regexp"
	"runtime"
	"sync"

	"github.com/spf13/pflag"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/ninjago/ninjalog"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
)

type Request struct {
	hash entities.Hash
	obj  *binding.EvalRequest
}
type Response struct {
	hash    entities.Hash
	obj     *binding.EvalResponse
	deletes map[string]bool
}
type LogEntry struct {
	*ninjalog.LogEntry

	req  Request
	resp Response

	buf          *bytes.Buffer
	validPatches int
}

type LogSection map[string]*LogEntry

type LogFile []LogSection

func LogFileParse(file io.Reader) (LogFile, error) {
	logs, err := ninjalog.Parse(file)
	if err != nil {
		return nil, err
	}

	result := make([]LogSection, len(logs))
	for i, l := range logs {
		result[i] = make(LogSection, len(l.Entries))
		for _, e := range l.Entries {
			result[i][e.Target] = &LogEntry{
				LogEntry: e,
				buf:      new(bytes.Buffer),
				req:      Request{hash: e.ReqHash},
				resp:     Response{hash: e.RespHash},
			}
		}
	}
	return result, nil
}

type buildCmd struct {
	*root.RootCmd
	*cmd.StoreProvider
	out          io.Writer
	numResolvers int
	logFile      string
	workDir      string
	exp          *regexp.Regexp
}

func NewCmd(rootCmd *root.RootCmd) *buildCmd {
	return &buildCmd{
		RootCmd: rootCmd,
		out:     os.Stdout,
	}
}

func (c *buildCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "build-log",
		Usage:   "[-w work_dir] [-r number of workers]] -f build.log|hash [regexp]",
		Aliases: []string{},
		Desc:    `Query and or validate the input build log. If the -w option is provided the build log patches are compared against the current state of the given working directory.  If a regexp is provided, report any matching patches of the regexp.`,
	}
}

func (c *buildCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVarP(&c.workDir, "workdir", "w", "", "Validate build log against work directory")
	fl.StringVarP(&c.logFile, "file", "f", "", "Build log file")
	fl.IntVarP(&c.numResolvers, "resolvers", "r", runtime.NumCPU(), "Number of resolver threads")
}

func (c *buildCmd) Exec(ctx context.Context, fl *pflag.FlagSet) (retErr error) {
	in := args.SingleArgOrDefault(fl, "")
	if in != "" {
		ex, err := regexp.Compile(in)
		if err != nil {
			return fmt.Errorf("failed to parse regular expression %q: %w", in, err)
		}
		c.exp = ex
	}

	openLogFile := func() (io.ReadCloser, error) {
		var file io.ReadCloser
		var err error
		file, err = os.Open(c.logFile)
		if err != nil {
			// Check if a blob
			var hash entities.Hash
			if _, err = hex.Decode(hash[:], []byte(c.logFile)); err != nil {
				return nil, err
			}

			file, err = c.Store.OpenBlob(ctx, hash)
			if err != nil {
				return nil, err
			}
		}
		return file, nil
	}

	file, err := openLogFile()
	if err != nil {
		return err
	}

	defer func() { retErr = entities.FirstError(retErr, file.Close()) }()
	logFile, err := LogFileParse(file)
	if err != nil {
		return err
	}
	log.SetFlags(0)
	err = c.Resolve(ctx, logFile)
	if err != nil {
		return err
	}

	for x, s := range logFile {
		validPatches := 0
		if _, err := fmt.Fprintf(c.out, "Section %d\n", x); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(c.out, "---------------------------------------------------------------------"); err != nil {
			return err
		}
		for k, e := range s {
			d := e.buf.String()
			if len(d) != 0 {
				if _, err := fmt.Fprintf(c.out, "%s\n", k); err != nil {
					return err
				}
				if _, err := fmt.Fprintf(c.out, "%s\n", d); err != nil {
					return err
				}
			}
			validPatches += e.validPatches
		}
		if _, err := fmt.Fprintln(c.out); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(c.out, "entries       : %d\n", len(s)); err != nil {
			return err
		}
		if c.workDir != "" {
			if _, err := fmt.Fprintf(c.out, "valid patches : %d\n", validPatches); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(c.out, "---------------------------------------------------------------------"); err != nil {
			return err
		}
	}
	return nil
}

func (c *buildCmd) checkRespPatches(e *LogEntry, patch entities.Patch, label string) {
	count := 0
	addLine := func(str string) {
		if count == 0 {
			fmt.Fprintln(e.buf, label)
			count = 1
		}
		fmt.Fprintf(e.buf, "%s\n", str)
	}
	for _, p := range patch {
		if p.Mode == entities.FileModeDir {
			continue
		} else if _, ok := e.resp.deletes[p.Path]; ok {
			continue
		}
		if c.workDir != "" {
			full := path.Join(c.workDir, p.Path)
			s, err := bindcas.Hash(full)
			if err != nil {
				addLine(fmt.Sprintf("Failed to get hash for %s - %s", full, err))
			} else if s != p.Hash {
				addLine("work file hash patch mismatch")
				addLine(fmt.Sprintf("%s %s", full, s))
				addLine(fmt.Sprintf("%s %s", p.Path, p.Hash.String()))
			} else {
				e.validPatches++
			}
		}
		if c.exp != nil {
			if c.exp.MatchString(p.Path) {
				addLine(fmt.Sprintf("%s %s %12d %s\n", p.Mode, p.Hash, p.Size, p.Path))
			}
		}
	}

}

func (c *buildCmd) checkResp(e *LogEntry) {
	c.checkRespPatches(e, e.resp.obj.ReadPatch, "READ")
	c.checkRespPatches(e, e.resp.obj.WritePatch, "WRITE")
}

func (c *buildCmd) resolveLogEntry(ctx context.Context, e *LogEntry) (err error) {
	req, err := fetchReq(ctx, c.Store, e.ReqHash)
	if err != nil {
		return err
	}
	resp, err := fetchResp(ctx, c.Store, e.RespHash)
	if err != nil {
		return err
	}

	e.req.obj = req
	e.resp.obj = resp
	e.resp.deletes = make(map[string]bool)
	for f := range e.resp.deletes {
		e.resp.deletes[f] = true
	}

	c.checkResp(e)
	return nil
}

func (c *buildCmd) resolvers(ctx context.Context) (chan *LogEntry, chan *LogEntry) {
	var wg sync.WaitGroup
	inChan := make(chan *LogEntry)
	outChan := make(chan *LogEntry)
	for i := 0; i < c.numResolvers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range inChan {
				if err := c.resolveLogEntry(ctx, e); err != nil {
					fmt.Printf("Failed to resolve entry %s %s : %s\n",
						e.ReqHash, e.RespHash, err)
				}
				outChan <- e
			}
		}()
	}
	go func() {
		wg.Wait()
		close(outChan)
	}()
	return inChan, outChan

}

func (c *buildCmd) Resolve(ctx context.Context, f LogFile) error {
	inCh, outCh := c.resolvers(ctx)
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for range outCh {
		}
	}()

	for _, s := range f {
		for _, v := range s {
			inCh <- v
		}
	}

	close(inCh)

	wg.Wait()
	return nil
}

func fetchReq(ctx context.Context, store entities.CASStore, hash entities.Hash) (*binding.EvalRequest, error) {
	buf, err := store.GetObject(ctx, hash)
	if err != nil {
		return nil, err
	}
	result := new(binding.EvalRequest)
	if err := result.Decode(hash, buf); err != nil {
		return nil, err
	}
	return result, nil
}

func fetchResp(ctx context.Context, store entities.CASStore, hash entities.Hash) (*binding.EvalResponse, error) {
	buf, err := store.GetObject(ctx, hash)
	if err != nil {
		return nil, err
	}
	result := new(binding.EvalResponse)
	if err := result.Decode(hash, buf); err != nil {
		return nil, err
	}
	return result, nil
}
