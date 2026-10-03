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

package diff

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"reflect"
	"runtime"
	"sort"
	"sync"

	"github.com/spf13/pflag"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/ninjago/ninjalog"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
)

type Hash = entities.Hash

type buildCmd struct {
	*root.RootCmd
	store        entities.CASStore
	ignMissing   bool
	verbose      bool
	quiet        bool
	numResolvers int
}

func NewBuildCmd(rootCmd *root.RootCmd) *buildCmd {
	return &buildCmd{
		RootCmd:    rootCmd,
		ignMissing: false,
		verbose:    false,
		quiet:      true,
	}
}

func (c *buildCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "diff-build",
		Usage:   "[-v] [--ignore] [-r NUM_CPU] log-1|hash-1 log-2|hash-2",
		Aliases: []string{},
		Desc:    `Show the diff between two builds based on logs`,
	}
}
func (c *buildCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.ignMissing, "ignore", "i", false, "Ignore missing entries.")
	fl.BoolVarP(&c.verbose, "verbose", "v", false, "Add verbose output on stderr.")
	fl.BoolVarP(&c.quiet, "quiet", "q", false, "Show only diffs in log")
	fl.IntVarP(&c.numResolvers, "resolvers", "r", runtime.NumCPU(), "Number of resolver threads")
}

type change struct {
	a, b *entities.PatchEntry
}
type patchDiff struct {
	adds    entities.Patch
	removes entities.Patch
	changes []change
}

func (p *patchDiff) isDiff() bool {
	if p == nil {
		return false
	}
	if len(p.adds) == 0 && len(p.removes) == 0 && len(p.changes) == 0 {
		return false
	}
	return true

}
func (p *patchDiff) log() {
	for _, y := range p.adds {
		log.Printf("%s %s %s %12d %s\n", ActionAdd, y.Mode, y.Hash, y.Size, y.Path)
	}

	for _, y := range p.removes {
		log.Printf("%s %s %s %12d %s\n", ActionRemove, y.Mode, y.Hash, y.Size, y.Path)
	}
	for _, y := range p.changes {
		log.Printf("%s %s %s %12d %s\n", ActionRemove, y.a.Mode, y.a.Hash, y.a.Size, y.a.Path)
		log.Printf("%s %s %s %12d %s\n", ActionAdd, y.b.Mode, y.b.Hash, y.b.Size, y.b.Path)
	}
}

// Single diff entry for 2 logEntries
type logDiffEntry struct {
	name string
	a    *ninjalog.LogEntry
	b    *ninjalog.LogEntry
	err  error
	rd   *patchDiff
	wr   *patchDiff
	in   *patchDiff
}

func (d *logDiffEntry) isDiff() bool {
	return d.rd.isDiff() || d.wr.isDiff()
}

func (d *logDiffEntry) logDelta() {

	log.Printf("%s\n", d.name)
	if d.in.isDiff() {
		log.Println("Input")
		d.in.log()
	}
	log.Println("Patches")
	if d.rd != nil {
		log.Println("Reads:")
		d.rd.log()

	}
	if d.wr != nil {
		log.Println("Writes:")
		d.wr.log()
	}
}

type sectionDiff struct {
	diffs   map[string]*logDiffEntry
	errors  map[string]*logDiffEntry
	adds    map[string]*ninjalog.LogEntry
	removes map[string]*ninjalog.LogEntry
}

func (c buildCmd) decode(l string) (result []map[string]*ninjalog.LogEntry, retErr error) {
	var file io.ReadCloser
	var err error
	file, err = os.Open(l)
	if err != nil {
		// Check if a blob
		var hash entities.Hash
		if _, err = hex.Decode(hash[:], []byte(l)); err != nil {
			return nil, err
		}

		file, err = c.store.OpenBlob(context.Background(), hash)
		if err != nil {
			return nil, err
		}

	}
	defer entities.CloseErrCheck(file, &retErr)

	logs, err := ninjalog.Parse(file)
	if err != nil {
		return nil, err
	}

	result = make([]map[string]*ninjalog.LogEntry, len(logs))
	for i, l := range logs {
		result[i] = make(map[string]*ninjalog.LogEntry, len(l.Entries))
		for _, e := range l.Entries {
			result[i][e.Target] = e
		}
	}
	return result, nil

}

func (c *buildCmd) diffPatch(p1, p2 entities.Patch) *patchDiff {
	// both p1 and p2 are sorted by the Path, so we can iterate them similar to merge sort
	diffs := &patchDiff{}
	i1, i2 := 0, 0
	for i1 < len(p1) || i2 < len(p2) {
		if i1 == len(p1) || ((i2 < len(p2)) && p1[i1].Path > p2[i2].Path) {
			if p2[i2].Mode != entities.FileModeDir {
				diffs.adds = append(diffs.adds, p2[i2])
			}
			i2++
		} else if i2 == len(p2) || p2[i2].Path > p1[i1].Path {
			if p1[i1].Mode != entities.FileModeDir {
				diffs.removes = append(diffs.removes, p1[i1])
			}
			i1++
		} else {
			if !reflect.DeepEqual(p1[i1], p2[i2]) {
				diffs.changes = append(diffs.changes, change{a: p1[i1], b: p2[i2]})
			}
			i1++
			i2++
		}
	}
	return diffs
}
func (c *buildCmd) diffRequests(ctx context.Context, o *logDiffEntry) {
	req1, err := binding.FetchObject[binding.EvalRequest](ctx, c.store, o.a.ReqHash)
	if err != nil {
		o.err = err
		return
	}
	req2, err := binding.FetchObject[binding.EvalRequest](ctx, c.store, o.b.ReqHash)
	if err != nil {
		o.err = err
		return
	}
	if !reflect.DeepEqual(req1.InputPatch, req2.InputPatch) {
		o.in = c.diffPatch(req1.InputPatch, req2.InputPatch)
	}
}
func (c *buildCmd) diffResponse(ctx context.Context, o *logDiffEntry) {
	resp1, err := binding.FetchObject[binding.EvalResponse](ctx, c.store, o.a.RespHash)
	if err != nil {
		o.err = err
		return
	}
	resp2, err := binding.FetchObject[binding.EvalResponse](ctx, c.store, o.b.RespHash)
	if err != nil {
		o.err = err
		return
	}
	if !reflect.DeepEqual(resp1.ReadPatch, resp2.ReadPatch) {
		o.rd = c.diffPatch(resp1.ReadPatch, resp2.ReadPatch)
	}
	if !reflect.DeepEqual(resp1.WritePatch, resp2.WritePatch) {
		o.wr = c.diffPatch(resp1.WritePatch, resp2.WritePatch)

	}
	//TODO Add runtime information for display

}
func (c *buildCmd) diff(ctx context.Context, o *logDiffEntry) {
	c.diffRequests(ctx, o)
	c.diffResponse(ctx, o)
}

func (c *buildCmd) differs(ctx context.Context, count int) (chan *logDiffEntry, chan *logDiffEntry) {
	var wg sync.WaitGroup
	inChan := make(chan *logDiffEntry)
	outChan := make(chan *logDiffEntry)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range inChan {
				c.diff(ctx, e)
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
func (c *buildCmd) diffSection(ctx context.Context, a, b map[string]*ninjalog.LogEntry) *sectionDiff {

	inCh, outCh := c.differs(ctx, c.numResolvers)

	out := &sectionDiff{
		diffs:   make(map[string]*logDiffEntry),
		errors:  make(map[string]*logDiffEntry),
		adds:    make(map[string]*ninjalog.LogEntry),
		removes: make(map[string]*ninjalog.LogEntry),
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for d := range outCh {
			if d.err != nil {
				out.errors[d.name] = d
			} else {
				out.diffs[d.name] = d
			}
		}
	}()

	for key, valA := range a {
		if valB, ok := b[key]; ok {
			inCh <- &logDiffEntry{a: valA, b: valB, name: key}
		} else {
			out.removes[key] = valA
		}
	}
	close(inCh)
	for key, valB := range b {
		if _, ok := a[key]; !ok {
			out.adds[key] = valB
		}
	}

	wg.Wait()
	return out
}

func (c *buildCmd) isUnique(o *logDiffEntry, section *sectionDiff) bool {
	// Check if this is an 'expected' diff
	diff := o.isDiff()
	inDiff := o.in.isDiff()

	if !diff || !inDiff {
		return true
	}
	if (len(o.in.adds) != 0) || (len(o.in.removes) != 0) {
		return true
	}
	if len(o.in.changes) == 0 {
		return true
	}
	for _, ch := range o.in.changes {
		if l, ok := section.diffs[ch.a.Path]; !ok {
			return true
		} else {
			if l.isDiff() {
				return false
			}
		}
	}
	return true
}
func (c *buildCmd) showSection(section *sectionDiff) {
	addMap := make(map[string]int)
	rmMap := make(map[string]int)
	chMap := make(map[string]int)

	sorted := make([]string, 0, len(section.diffs))
	for k := range section.diffs {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)

	diffStr := func(val bool) string {
		if val {
			return "!="
		}
		return "=="
	}
	uniqStr := func(val bool) string {
		if val {
			return "*"
		}
		return " "
	}

	for _, k := range sorted {
		o := section.diffs[k]

		diff := o.isDiff()
		inDiff := o.in.isDiff()
		uniqDiff := c.isUnique(o, section)

		if !c.quiet || (c.quiet && diff) {
			fmt.Printf("%s%s%s %s %s %s %s %s %s %s\n",
				o.a.RunType, o.b.RunType, uniqStr(uniqDiff),
				o.a.ReqHash.String(), diffStr(inDiff), o.b.ReqHash.String(),
				o.a.RespHash.String(), diffStr(diff), o.b.RespHash.String(),
				o.name)
		}

		// Send delta to stderr
		if diff && c.verbose {
			o.logDelta()
		}
		if uniqDiff {
			if o.rd != nil {
				for _, y := range o.rd.adds {
					addMap[y.Path]++
				}
				for _, y := range o.rd.removes {
					rmMap[y.Path]++
				}
				for _, y := range o.rd.changes {
					chMap[y.a.Path]++
				}
			}
			if o.wr != nil {
				for _, y := range o.wr.adds {
					addMap[y.Path]++
				}
				for _, y := range o.wr.removes {
					rmMap[y.Path]++
				}
				for _, y := range o.wr.changes {
					chMap[y.a.Path]++
				}
			}
		}
	}

	showSection := func(hdr string, m map[string]*ninjalog.LogEntry) {
		sorted := make([]string, 0, len(m))
		for k := range m {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		fmt.Printf("%s (%d) : \n", hdr, len(m))
		for _, k := range sorted {
			o := m[k]
			fmt.Printf("%s %s %s \n",
				o.ReqHash.String(), o.RespHash.String(), k)

		}

	}
	showSection("Adds", section.adds)
	if !c.ignMissing {
		showSection("Removes", section.removes)
	}
	fmt.Printf("Errors (%d)\n", len(section.errors))
	for name, o := range section.errors {
		fmt.Printf("%s %s %s %s %s %s\n",
			o.a.ReqHash.String(), o.b.ReqHash.String(),
			o.a.RespHash.String(), o.b.RespHash.String(), name,
			o.err)
	}

	showMap := func(hdr string, m map[string]int) {
		sorted := make([]string, 0, len(m))
		for k := range m {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		fmt.Printf("%s (%d) : \n", hdr, len(m))
		for _, k := range sorted {
			fmt.Printf("   %s\n", k)
		}
	}

	fmt.Printf("\nSummation\n")
	showMap("Adds", addMap)
	showMap("Removes", rmMap)
	showMap("Changes", chMap)
}
func (c *buildCmd) diffBuild(ctx context.Context, a, b []map[string]*ninjalog.LogEntry) {
	aSections := len(a)
	bSections := len(b)
	if aSections != bSections {
		fmt.Printf("Logs contain different number of sections %d %d\n",
			aSections, bSections)
		return
	}
	for i := 0; i < aSections; i++ {
		fmt.Printf("Section %d\n", i)
		fmt.Println("--------------------------------------------------------------------")
		out := c.diffSection(ctx, a[i], b[i])
		c.showSection(out)
		fmt.Println("--------------------------------------------------------------------")
	}
}
func (c *buildCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	log1, log2 := args.EnsureTwoArgs(fl)

	var wg sync.WaitGroup
	var l1, l2 []map[string]*ninjalog.LogEntry
	var err1, err2 error

	wg.Add(1)
	go func() {
		defer wg.Done()
		l1, err1 = c.decode(log1)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		l2, err2 = c.decode(log2)
	}()

	wg.Wait()

	if err1 != nil {
		return err1
	}
	if err2 != nil {
		return err2
	}

	log.SetFlags(0)
	c.diffBuild(ctx, l1, l2)
	return nil
}
