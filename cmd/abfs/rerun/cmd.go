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
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/args"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
)

const tapNoCGo = "tap-no-cgo"

type rerunCmd struct {
	*root.RootCmd
	*cmd.ConfigProvider
	*cmd.StoreProvider
	out             io.Writer
	overrideCommand string
	ignorePatch     bool
	withTaps        bool
	noABFS          bool
	respHash        entities.Hash
	nsJailBin       string
	nsJailCfg       string
	topDir          string
	store           entities.CASStore
	command         string
	resp            *binding.EvalResponse
	req             *binding.EvalRequest
	envvars         []string
	debug           bool
	cpuProf         string
	keep            bool
	noRespDiff      bool
	noTap           bool
	writeResp       bool
	showRunTimeInfo bool
	rootDir         string
	outDir          string
	tmpDir          string
}

func NewCmd(rootCmd *root.RootCmd) *rerunCmd {
	return &rerunCmd{RootCmd: rootCmd}
}

func (c *rerunCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:  "rerun",
		Usage: "[flags] <eval-response-hash> -t $CHROOT",
		Desc:  `Rerun a specific build command and check the result based on the given response.  Rerun requires a chroot reference ($CHROOT) which must have been previously setup via nsjail`,
	}
}

func (c *rerunCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVar(&c.ignorePatch, "ignore-patch", false, "Ignore the read patch in the response")
	fl.BoolVar(&c.withTaps, "with-taps", false, "Print all the taps")
	fl.BoolVar(&c.debug, "debug", false, "Print all the FUSE request and responses")
	fl.BoolVar(&c.noABFS, "no-abfs", false, "Do not run the command in abfs environment")
	fl.StringVarP(&c.overrideCommand, "command", "c", "", "Override the command")
	fl.StringVarP(&c.nsJailBin, "nsjail-bin", "j", "prebuilts/build-tools/linux-x86/bin/nsjail", "The path of an nsjail to run")
	fl.StringVarP(&c.nsJailCfg, "nsjail-cfg", "f", "tools/treble/build/sandbox/nsjail.cfg", "The path to nsjail configuration")
	fl.StringVarP(&c.topDir, "top", "t", ".", "The path to top directory of android source")
	fl.BoolVarP(&c.keep, "keep", "k", false, "Do not umount and remove the mount point")
	fl.BoolVar(&c.noRespDiff, "no-diff", false, "Hide the response diff")
	fl.BoolVar(&c.noTap, "no-tap", false, "Do not monitor file-system for changes")
	fl.BoolVarP(&c.writeResp, "write", "w", false, "Write the response object")

	fl.StringVar(&c.cpuProf, "cpu-pprof", "", "Enable cpu pprof, writing data to this file")
	fl.BoolVarP(&c.showRunTimeInfo, "run-info", "", false, "Display runtime information")
}

func (c *rerunCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	c.respHash = entities.NewHash(args.EnsureSingleArg(fl))

	c.out = os.Stdout
	err := c.run(ctx)
	if errors.Is(err, ErrInconsistent) {
		return clierr.CLIErrNoMessage(1)
	}
	return err
}

func (c *rerunCmd) run(ctx context.Context) error {
	var err error
	c.resp, err = binding.FetchObject[binding.EvalResponse](ctx, c.Store, c.respHash)
	if err != nil {
		return fmt.Errorf("cannot fetch eval-response: %w", err)
	}

	c.req, err = binding.FetchObject[binding.EvalRequest](ctx, c.Store, c.resp.EvalRequest)
	if err != nil {
		return fmt.Errorf("cannot fetch eval-request %s: %w", c.resp.EvalRequest, err)
	}

	c.envvars, err = c.fetchEnvVars(ctx, c.req.EnvVars)
	if err != nil {
		return fmt.Errorf("cannot fetch environment variables %s: %w", c.req.EnvVars, err)
	}

	return c.rerun(ctx)
}

func (c *rerunCmd) Printf(format string, a ...any) {
	_, _ = fmt.Fprintf(c.out, format, a...)
}

func (c *rerunCmd) Println(a ...any) {
	_, _ = fmt.Fprintln(c.out, a...)
}

var ErrInconsistent = errors.New("the rerun is inconsistent with the specified response")

func (c *rerunCmd) rerun(ctx context.Context) error {
	log.Printf("Response: %s", c.respHash)
	log.Printf("Request: %s", c.resp.EvalRequest)

	c.command = c.req.Command
	if c.overrideCommand != "" {
		c.command = c.overrideCommand
	}

	dir, err := os.MkdirTemp("", "rerun-mnt-*")
	if err != nil {
		return fmt.Errorf("cannot create temp dir: %s", err)
	}
	if c.keep {
		log.Println("Keeping the root directory", dir)
	} else {
		defer func() {
			if err := os.RemoveAll(dir); err != nil {
				log.Printf("cannot remove the tmp dir %s: %s", dir, err)
			}
		}()
	}
	c.rootDir = dir
	c.outDir = c.GetEnv(entities.OutDirVar, "out")
	c.tmpDir = c.GetEnv("TMPDIR", "")

	var newResp *binding.EvalResponse
	if c.noABFS {
		newResp, err = c.evalNoABFS(ctx)
	} else {
		newResp, err = c.evalABFS()
	}
	if err != nil {
		return fmt.Errorf("FAILED: %w", err)
	}

	if newResp == nil {
		// No response to show diff
		return nil
	}

	if c.writeResp {
		if err := newResp.Encode(); err != nil {
			return fmt.Errorf("failed to encode response: %w", err)
		}
		if err := c.Store.PutObject(ctx, newResp.Hash, newResp.Encoded); err != nil {
			return fmt.Errorf("failed to write response: %w", err)
		}
		c.Printf("New Response: %s\n", newResp.Hash)
	}
	c.Printf("Duration:\n    prev  %f\n    rerun %f\n", c.resp.RunInfo.RunDuration, newResp.RunInfo.RunDuration)

	if c.showRunTimeInfo {
		binding.DiffRunInfo(c.out, c.resp, newResp)
	}
	if !c.noRespDiff {
		if c.respDiff(newResp) {
			return ErrInconsistent
		}
	}

	return nil
}

func (c *rerunCmd) nsjailOpts(root string) ([]string, error) {
	nsjailOpts := make([]string, 0, 2*len(c.envvars))
	for _, kv := range c.envvars {
		nsjailOpts = append(nsjailOpts, "--env", kv)
	}
	nsjailOpts = append(nsjailOpts, "-q")
	nsjailOpts = append(nsjailOpts, "--chroot", c.rootDir)

	chrootPath := c.detectChroot()
	if chrootPath == "" {
		nsjailOpts = append(nsjailOpts, "-R", "/lib")
		nsjailOpts = append(nsjailOpts, "-R", "/lib64")
		nsjailOpts = append(nsjailOpts, "-R", "/bin")
		nsjailOpts = append(nsjailOpts, "-R", "/sbin")
		nsjailOpts = append(nsjailOpts, "-R", "/usr")
		nsjailOpts = append(nsjailOpts, "-R", "/etc/alternatives")
	} else {
		seen := make(map[string]struct{})
		for _, p := range c.resp.ReadPatch {
			if p.Mode == entities.FileModeDir {
				continue
			}
			if strings.HasPrefix(p.Path, chrootPath) {
				d := strings.SplitN(p.Path[len(chrootPath):], "/", 2)[0]
				if _, ok := seen[d]; ok {
					continue
				}
				nsjailOpts = append(nsjailOpts, "-R", path.Join(root, chrootPath, d)+":/"+d)
				seen[d] = struct{}{}
			}
		}
		if err := os.Symlink("/usr/bin", path.Join(c.rootDir, "bin")); err != nil {
			return nil, err
		}
		if err := os.Symlink("/usr/lib", path.Join(c.rootDir, "lib")); err != nil {
			return nil, err
		}
		if err := os.Symlink("/usr/lib64", path.Join(c.rootDir, "lib64")); err != nil {
			return nil, err
		}
	}
	return nsjailOpts, nil
}

func (c *rerunCmd) respDiff(newResp *binding.EvalResponse) bool {
	inconsistent := false
	inconsistent = c.writePatchDiff("Reads", c.resp.ReadPatch, newResp.ReadPatch) || inconsistent
	inconsistent = c.writePatchDiff("Writes", c.resp.WritePatch, newResp.WritePatch) || inconsistent

	if newResp.ExitCode != c.resp.ExitCode {
		inconsistent = true
	}
	if inconsistent {
		c.writeListDiff("Deletes", c.resp.Deletes, newResp.Deletes)
		c.Printf("Command finished with exit-code: %d\n", newResp.ExitCode)

		c.Printf("Targets:\n")
		for _, t := range c.req.Targets {
			c.Printf("  %s\n", t)
		}
		c.Println()
		c.Printf("Command: %s\n", c.command)
		c.Println()

		if newResp.Stdout != "" {
			c.Println("Stdout:")
			c.printOutput(newResp.Stdout)
		}
		if newResp.Stderr != "" {
			c.Println("Stderr:")
			c.printOutput(newResp.Stderr)
		}
	}
	return inconsistent
}

func (c *rerunCmd) printOutput(content string) {
	content = strings.TrimSuffix(content, "\n")
	for _, line := range strings.Split(content, "\n") {
		c.Println("| ", line)
	}
	c.Println()
}

func (c *rerunCmd) writePatchDiff(name string, a, b entities.Patch) bool {
	patchToMap := func(p entities.Patch) map[string]*entities.PatchEntry {
		r := make(map[string]*entities.PatchEntry, len(p))
		for _, pe := range p {
			// Ignore directories because their hashes represent the dir listing
			if pe.Mode == entities.FileModeDir {
				continue
			}
			r[pe.Path] = pe
		}
		return r
	}
	mismatches := make([]string, 0)
	addMismatch := func(op byte, pe *entities.PatchEntry) {
		mismatches = append(mismatches, fmt.Sprintf("  %c %12s %s %-6o %s", op, pe.State, pe.Hash, pe.Mode, pe.Path))
	}

	aMap := patchToMap(a)
	bMap := patchToMap(b)
	for n, pe := range aMap {
		pe2, ok := bMap[n]
		if !ok {
			addMismatch('-', pe)
		} else if *pe != *pe2 {
			addMismatch('-', pe)
			addMismatch('+', pe2)
		}
	}
	for n, pe := range bMap {
		_, ok := aMap[n]
		if !ok {
			addMismatch('+', pe)
		}
	}
	if len(mismatches) == 0 {
		c.Printf("%s No diff\n", name)
		return false
	}
	c.Printf("%s MISMATCH:\n", name)
	for _, m := range mismatches {
		c.Println(m)
	}
	return true
}

func (c *rerunCmd) writeListDiff(name string, a, b []string) bool {
	listToMap := func(p []string) map[string]struct{} {
		r := make(map[string]struct{}, len(p))
		for _, s := range p {
			r[s] = struct{}{}
		}
		return r
	}
	mismatches := make([]string, 0)
	addMismatch := func(op byte, p string) {
		mismatches = append(mismatches, fmt.Sprintf("  %c %s", op, p))
	}

	aMap := listToMap(a)
	bMap := listToMap(b)
	for s := range aMap {
		if _, ok := bMap[s]; !ok {
			addMismatch('-', s)
		}
	}
	for s := range bMap {
		if _, ok := aMap[s]; !ok {
			addMismatch('+', s)
		}
	}
	if len(mismatches) == 0 {
		c.Printf("%s No diff\n", name)
		return false
	}
	c.Printf("%s MISMATCH:\n", name)
	for _, m := range mismatches {
		c.Println(m)
	}
	return true
}

func waitForFile(fileName string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, err := os.Stat(fileName)
		if err == nil {
			return nil
		}
		if !entities.IsNotExist(err) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("timeout exceeded while waiting for file: %s", fileName)
}

func (c *rerunCmd) detectChroot() string {
	for _, p := range c.resp.ReadPatch {

		if i := strings.Index(p.Path, "/chroot/usr"); i != -1 {
			return p.Path[:i+len("/chroot/")]
		}
	}
	return ""
}

func (c *rerunCmd) fetchEnvVars(ctx context.Context, hash entities.Hash) ([]string, error) {
	buf, err := c.Store.GetObject(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("cannot fetch env-vars %s: %w", hash, err)
	}

	o := new(binding.EnvVars)
	if err := o.Decode(hash, buf); err != nil {
		return nil, err
	}

	envvars := make([]string, 0, len(o.Vars))
	for k, v := range o.Vars {
		v = strings.ReplaceAll(v, "CWD", entities.SrcDir)
		envvars = append(envvars, fmt.Sprintf("%s=%s", k, v))
	}
	return envvars, nil
}

func (c *rerunCmd) GetEnv(key, defaultValue string) string {
	for _, kv := range c.envvars {
		if strings.HasPrefix(kv, key+"=") {
			return strings.TrimPrefix(kv, key+"=")
		}
	}
	return defaultValue
}

func (c *rerunCmd) writePathInterposer(filePath string) error {
	// FIXME: Temporary workaround; remove once it is no longer needed.
	pathInterposer := []string{
		"/src/prebuilts/jdk/jdk21/linux-x86/bin",
		path.Join("/src", c.outDir, "host/linux-x86/bin"),
		"/src/prebuilts/clang/host/linux-x86/llvm-binutils-stable",
		"/src/prebuilts/asuite/acloud/linux-x86",
		"/src/prebuilts/asuite/aidegen/linux-x86",
		"/src/prebuilts/asuite/atest/linux-x86",
		"/src/prebuilts/jdk/jdk21/linux-x86/bin",
		"/src/build/soong/bin",
		"/src/build/bazel/bin",
		"/src/development/scripts",
		"/src/prebuilts/devtools/tools",
		"/src/prebuilts/misc/linux-x86/dtc",
		"/src/prebuilts/misc/linux-x86/libufdt",
		"/src/prebuilts/android-emulator/linux-x86_64",
		"/usr/local/sbin",
		"/usr/local/bin",
		"/usr/sbin",
		"/usr/bin",
		"/sbin",
		"/bin",
	}
	return os.WriteFile(filePath, []byte(strings.Join(pathInterposer, ":")), 0644)
}
