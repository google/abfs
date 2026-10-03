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

package diags

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/spf13/pflag"

	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
)

type diagCmd struct {
	*root.RootCmd
	diff         bool
	outFile      string
	numResolvers int
}

func NewCmd(rootCmd *root.RootCmd) *diagCmd {
	return &diagCmd{RootCmd: rootCmd}
}

type NodeInfo struct {
	IsDir  bool          `json:"is_dir"`
	Hash   entities.Hash `json:"hash"`
	Loaded bool          `json:"loaded"`
	Dirty  bool          `json:"dirty"`
}

type NodeDir struct {
	Path  string `json:"root_path"`
	Nodes map[string]*NodeInfo
}

type NodeData struct {
	Path string
	Info *NodeInfo
}

type NodeDiff struct {
	A NodeInfo `json:"nodeA"`
	B NodeInfo `json:"nodeB"`
}
type DiffData struct {
	FileA      string              `json:"fileA"`
	FileB      string              `json:"fileB"`
	NodeDiffs  map[string]NodeDiff `json:"node_difs"`
	NodeNotInA map[string]NodeInfo `json:"nodes_not_in_a"`
	NodeNotInB map[string]NodeInfo `json:"nodes_not_in_b"`
}

func createNodeDir(path string) *NodeDir {
	return &NodeDir{
		Path:  path,
		Nodes: make(map[string]*NodeInfo),
	}
}

func createDiffData(fileA, fileB string) *DiffData {
	return &DiffData{
		FileA:      fileA,
		FileB:      fileB,
		NodeDiffs:  make(map[string]NodeDiff),
		NodeNotInA: make(map[string]NodeInfo),
		NodeNotInB: make(map[string]NodeInfo),
	}

}
func (c *diagCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "diags",
		Usage:   "dir [-o output.json] [-r NUM_CPU] [--diff f1.json f2.json] ",
		Aliases: []string{},
		Desc:    `Show diagnostics`,
	}
}
func (c *diagCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.StringVarP(&c.outFile, "output", "o", "", "Output file")
	fl.BoolVar(&c.diff, "diff", false, "Diff to output files")
	fl.IntVarP(&c.numResolvers, "resovlers", "r", 20, "Number of resolver threads")
}

type fileData struct {
	path string
	info fs.FileInfo
}

func nodeResolvers(ctx context.Context, count int) (chan *fileData, chan *NodeData) {
	var wg sync.WaitGroup
	inChan := make(chan *fileData)
	outChan := make(chan *NodeData)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			for f := range inChan {

				d := &NodeInfo{IsDir: f.info.IsDir()}
				// TODO: probably remove hash here to avoid snapshot?
				d.Hash, _ = bindcas.Hash(f.path)
				d.Loaded, _ = bindcas.Loaded(f.path)
				d.Dirty, _ = bindcas.Dirty(f.path)

				outChan <- &NodeData{f.path, d}
			}
			wg.Done()
		}()
	}
	go func() {
		wg.Wait()
		close(outChan)
	}()
	return inChan, outChan
}

func (c *diagCmd) create(fl *pflag.FlagSet) *NodeDir {
	dir := filepath.Clean(args.EnsureSingleArg(fl))
	out := createNodeDir(dir)
	ctx := context.Background()
	inCh, nodeCh := nodeResolvers(ctx, c.numResolvers)

	go func() {
		err := filepath.Walk(dir, func(path string, info fs.FileInfo, err error) error {
			if err != nil {
				return err
			}
			inCh <- &fileData{path, info}
			return nil
		})
		if err != nil {
			log.Printf("Error while walking directory: %s", err)
		}
		close(inCh)
	}()

	for n := range nodeCh {
		out.Nodes[strings.TrimPrefix(n.Path, dir)] = n.Info
	}
	return out

}
func (c *diagCmd) runDiff(fl *pflag.FlagSet) *DiffData {
	fA, fB := args.EnsureTwoArgs(fl)
	fileA, err := os.Open(fA)
	if err != nil {
		fmt.Printf("failed to open file %s (%s)\n", fA, err)
		return nil
	}
	defer func() {
		if err := fileA.Close(); err != nil {
			fmt.Printf("failed to close file %s (%s)\n", fA, err)
		}
	}()
	fileB, err := os.Open(fB)
	if err != nil {
		fmt.Printf("failed to open file %s (%s)\n", fB, err)
		return nil
	}
	defer func() {
		if err := fileB.Close(); err != nil {
			fmt.Printf("failed to close file %s (%s)\n", fB, err)
		}
	}()

	dA := createNodeDir("")
	dB := createNodeDir("")
	if err := json.NewDecoder(fileA).Decode(&dA); err != nil {
		fmt.Printf("failed to decode file %s (%s)\n", fA, err)
		return nil
	}
	if err := json.NewDecoder(fileB).Decode(&dB); err != nil {
		fmt.Printf("failed to decode file %s (%s)\n", fB, err)
		return nil
	}

	out := createDiffData(fA, fB)

	for key, valA := range dA.Nodes {
		if valB, ok := dB.Nodes[key]; ok {
			if *valA != *valB {
				out.NodeDiffs[key] = NodeDiff{A: *valA, B: *valB}
			}
		} else {
			out.NodeNotInB[key] = *valA

		}
	}
	for key, valB := range dB.Nodes {
		if _, ok := dA.Nodes[key]; !ok {
			out.NodeNotInA[key] = *valB
		}
	}

	return out
}
func (c *diagCmd) Exec(ctx context.Context, fl *pflag.FlagSet) (retErr error) {
	var err error
	file := os.Stdout
	if c.outFile != "" {
		file, err = os.Create(c.outFile)
		if err != nil {
			fmt.Printf("failed to open output file %s (%s)", c.outFile, err)
		}
		defer func() { retErr = entities.FirstError(retErr, file.Close()) }()
	}
	enc := json.NewEncoder(file)
	enc.SetIndent("", "    ")

	if c.diff {
		err = enc.Encode(c.runDiff(fl))
	} else {
		err = enc.Encode(c.create(fl))
	}
	if err != nil {
		return fmt.Errorf("error encoding json: %w", err)
	}
	return nil
}
