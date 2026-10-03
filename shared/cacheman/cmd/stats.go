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

package cacheman

import (
	"context"
	"fmt"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/go-git/go-git/v5/plumbing/color"
	"github.com/spf13/pflag"
	"golang.org/x/term"

	"github.com/google/abfs/shared/cacheman/client"
	"github.com/google/abfs/shared/cacheman/protocol"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
)

const (
	clearScreenSequence = "\033[2J\033[H"
	enterAltScreen      = "\033[?1049h"
	exitAltScreen       = "\033[?1049l"
)

type statsCmd struct {
	*cachemanCmd
	interval    int
	useColor    bool
	prevNumbers []int64
	newNumbers  []int64
}

func NewStatsCmd(c *cachemanCmd) *statsCmd {
	return &statsCmd{cachemanCmd: c}
}

func (c *statsCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "stats",
		Usage:   "",
		Aliases: []string{"stat"},
		Desc:    `Show the dag-sync stats.`,
	}
}

func (c *statsCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.IntVarP(&c.interval, "interval", "i", 1, "Number of seconds to wait between stats prints")
}

func (c *statsCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	args.EnsureNoArgs(fl)

	return c.run(ctx)
}

func (c *statsCmd) run(ctx context.Context) error {
	cacheDir := c.Config.CacheDir
	client := client.NewHTTPClient(cacheDir)

	if term.IsTerminal(1) {
		c.useColor = true
	} else {
		c.useColor = false
	}

	if c.useColor {
		fmt.Println(enterAltScreen)
		defer fmt.Println(exitAltScreen)
	}

	ticker := time.NewTicker(time.Duration(c.interval) * time.Second)
	defer ticker.Stop()

	c.tick(client)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			c.tick(client)
		}
	}
}

func (c *statsCmd) tick(client *client.HttpClient) {
	stats, err := client.Stats()

	c.clearScreen()

	if err != nil {
		fmt.Println(err)
	} else {
		c.printStats(stats)
	}
}

func (c *statsCmd) color(clr string, format string, args ...any) string {
	if !c.useColor {
		return fmt.Sprintf(format, args...)
	}
	newArgs := make([]any, 0, len(args)+2)
	newArgs = append(newArgs, clr)
	newArgs = append(newArgs, args...)
	newArgs = append(newArgs, color.Reset)
	return fmt.Sprintf("%s"+format+"%s", newArgs...)
}

func (c *statsCmd) history(n int64) (int64, bool) {
	c.newNumbers = append(c.newNumbers, n)

	if c.prevNumbers == nil {
		return 0, false
	}
	return c.prevNumbers[len(c.newNumbers)-1], true
}

func (c *statsCmd) number(n int64, width int, human func(int64) string) string {
	prev, ok := c.history(n)
	var format string
	var clr string
	var args []any
	if ok && prev != n {
		args = []any{human(n), human(n - prev)}
		clr = color.Bold
		if n > prev {
			format = "%s (+%s)"
		} else {
			format = "%s (%s)"
		}
	} else {
		if n == 0 {
			clr = color.Faint
		} else {
			clr = color.Normal
		}
		args = []any{human(n)}
		format = "%s"
	}
	text := c.color(clr, format, args...)
	if c.useColor {
		width += len(clr) + len(color.Reset)
	}
	return fmt.Sprintf(fmt.Sprintf("%%-%ds", width), text)
}

func (c *statsCmd) vol(size int64, width int) string {
	return c.number(size, width, func(i int64) string {
		return humanize.Bytes(uint64(i))
	})
}

func (c *statsCmd) cell(n int64) string {
	return c.number(n, 20, humanize.Comma)
}

func (c *statsCmd) title(format string, args ...any) string {
	return c.color(color.Cyan, format, args...)
}

func (c *statsCmd) clearScreen() {
	if c.useColor {
		fmt.Print(clearScreenSequence)
	} else {
		fmt.Println()
	}
}

func (c *statsCmd) resetNumbers() {
	c.prevNumbers = c.newNumbers
	c.newNumbers = make([]int64, 0, len(c.prevNumbers))
}

func humanTime(t time.Time) string {
	if time.Since(t) < time.Second {
		return "< 1 second ago"
	}
	return humanize.Time(t)
}

func (c *statsCmd) printStats(stats protocol.CacheManStats) {
	c.resetNumbers()
	var gc string
	if !stats.GCStart.After(stats.GCEnd) {
		gc = fmt.Sprintf("Last ran %s", humanTime(stats.GCEnd))
	} else {
		gc = fmt.Sprintf("Running since %s", humanTime(stats.GCStart))
	}
	var evict string
	if !stats.EvictStart.After(stats.EvictEnd) {
		evict = fmt.Sprintf("Last ran %s", humanTime(stats.EvictEnd))
	} else {
		evict = fmt.Sprintf("Running since %s", humanTime(stats.EvictStart))
	}
	var connected string
	if stats.Connected {
		connected = c.color(color.Green, "%-20s", "Connected")
	} else {
		connected = c.color(color.Red, "%-20s", "Disconnected")
	}
	fmt.Printf("%s %s %s %s\n", c.title("Status       "), connected, c.title("GC      "), gc)
	fmt.Printf("%s %-20s %s %s\n", c.title("Oldest Object"), humanTime(stats.OldestObject), c.title("Eviction"), evict)
	fmt.Printf("%s %s %s %s %s %s %s %s\n", c.title("Total Size   "), c.vol(stats.TotalSize, 20), c.title("LRU Size     "), c.vol(stats.LRUSize, 20), c.title("Local Count"), c.cell(stats.LocalCount), c.title("Index Count"), c.cell(stats.IndexCount))
	fmt.Printf("%s %s %s %s\n", c.title("Sent         "), c.vol(stats.BytesSent, 20), c.title("Received"), c.vol(stats.BytesReceived, 20))
	fmt.Printf("%14s %-20s %8s %s\n", "", humanTime(stats.LastSent), "", humanTime(stats.LastReceived))
	fmt.Printf("%s %s %s %s\n", c.title("Broken Links "), c.number(stats.BrokenLinks, 20, humanize.Comma), c.title("Missings"), c.cell(stats.MissingObjects))
	fmt.Println()

	fmt.Println(c.title("%13s %-20s %-20s %-20s %-20s %-20s", "", "Queued", "Pushing", "Requests", "Blocked", "Failed"))
	printPush := func(title string, pushStat protocol.PushGroupStat) {
		fmt.Print(c.title("%-12s  ", title))
		fmt.Print(c.cell(pushStat.Queued), " ")
		fmt.Print(c.cell(pushStat.Pushing), " ")
		fmt.Print(c.cell(pushStat.Requests), " ")
		fmt.Print(c.cell(pushStat.Blocked), " ")
		fmt.Print(c.cell(pushStat.Failed), "\n")
	}
	printPush("Push Blob", stats.PushBlobs)
	printPush("Push Object", stats.PushObjects)
	printPush("Push Tree", stats.PushTrees)
	printPush("Push Map", stats.PushMaps)
	printPush("Push Ref", stats.PushRefs)

	fmt.Println()
	printQueue := func(title string, queueStat *protocol.QueueStat) {
		fmt.Print(c.title("%-12s  ", title))
		fmt.Print(c.cell(queueStat.Input.Load()-queueStat.Output.Load()), " ")
		fmt.Print(c.cell(queueStat.Active.Load()), " ")
		fmt.Print(c.cell(queueStat.Input.Load()), " ")
		fmt.Print(c.cell(queueStat.Output.Load()), " ")
		fmt.Print(c.cell(queueStat.Errors.Load()), "\n")
	}
	fmt.Println(c.title("%13s %-20s %-20s %-20s %-20s %-20s", "", "Queued", "Fetching", "Input", "Output", "Error"))
	printQueue("Fetch Blob", stats.FetchBlobs)
	printQueue("Fetch Tree", stats.FetchTrees)
	printQueue("Fetch Object", stats.FetchObjects)
	printQueue("Fetch Map", stats.FetchMaps)
	fmt.Print(c.title("%-12s  ", "Execution"))
	fmt.Print(c.cell(stats.Execute.Request-stats.Execute.Failed-stats.Execute.Success), " ")
	fmt.Print(c.cell(stats.Execute.Request), " ")
	fmt.Print(c.cell(stats.Execute.Success), " ")
	fmt.Print(c.cell(stats.Execute.Failed), "\n")
}
