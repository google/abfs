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

package ninjago

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/ninja-go/build"
	"github.com/google/ninja-go/graph"
	"github.com/google/ninja-go/parse"

	"github.com/google/abfs/binding"
	"github.com/google/abfs/shared/bindcas"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/store"
)

const (
	remoteReqPushTimeout   = 2 * time.Minute
	remoteRespFetchTimeout = 30 * time.Minute
)

func (c *ABFSBackend) isRemotable(step *build.Step) bool {
	if !c.useRemote {
		return false
	}
	if _, found := c.remotableRules[step.Node.Build.RuleName]; found {
		return true
	}
	return false
}

var remoteBaseRoots = []string{
	"prebuilts/build-tools",

	// FIXME get these from PATH
	"$OUT_DIR/.path",

	// FIXME get from JAVA_HOME
	"prebuilts/jdk/jdk*",

	// FIXME detect the versions from input
	"prebuilts/rust/linux-x86/*",
	"prebuilts/gcc/linux-x86/host/*",
	"prebuilts/clang/host/linux-x86",

	"prebuilts/go/linux-x86",

	"external/kotlinc",

	"prebuilts/rust-toolchain/linux-x86",
}

var remoteBaseFiles = []string{
	"$OUT_DIR/.path_interposer",

	// TODO: Kotlinc rules has hidden dependency to kotlinx-coroutines-core-jvm.jar
	"external/kotlinc/lib/kotlinx-coroutines-core-jvm.jar",

	// TODO: MultiDex java compilations have hidden dep on this
	"art/test/MultiDex/main.list",
	"art/test/ProfileTestMultiDex/main.list",
	"art/test/MultiDexModifiedSecondary/main.list",

	// kotlin needs these currently reading them from depfile (g.java.r8)
	"build/make/core/proguard/kotlin.flags",
	"frameworks/base/packages/SystemUI/proguard_common.flags",
	"frameworks/base/packages/SystemUI/proguard_kotlin.flags",

	// TODO: turbine-apt-res.jar hidden deps on cobalt schemas
	"packages/modules/AdServices/adservices/libraries/cobalt/schemas/com.android.cobalt.data.CobaltDatabase/*.json",
	"packages/modules/AdServices/adservices/service-core/schemas/*/*.json",

	// TODO: ndk stub generation hidden dep on afdo
	"toolchain/pgo-profiles/sampling/14213865-trunk/*.afdo",

	// TODO: Font objects generation hidden dep on ttf files
	"device/google/cuttlefish/host/libs/confui/*.ttf",
}

func (c *ABFSBackend) addPathInterposer(ctx context.Context, filePath string, patch *entities.Patch) error {
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
	content := []byte(strings.Join(pathInterposer, ":"))
	hash, size, err := store.StoreBlob(ctx, c.abfs.Store(), content)
	if err != nil {
		return err
	}
	*patch = append(*patch, &entities.PatchEntry{
		Path: filePath,
		Hash: hash,
		Mode: entities.FileModeRegular,
		Size: size,
	})
	return nil
}

func (c *ABFSBackend) addRemoteRoot(root string) error {

	root = path.Clean(root)
	stat, err := os.Stat(root)

	if err != nil {
		return entities.IgnoreNotExistErr(err)
	}
	if !stat.IsDir() {
		return nil
	}
	hash, err := bindcas.Hash(root)
	if err != nil {
		return entities.IgnoreNotExistErr(err)
	}
	c.remoteBasePatch.Patch = append(c.remoteBasePatch.Patch, &entities.PatchEntry{
		Path: root,
		Hash: hash,
		Mode: entities.FileModeDir,
	})
	return nil
}

func (c *ABFSBackend) loadRemote() error {
	c.setupRemotableRules()

	c.remoteBasePatch = &binding.PatchObject{
		Version: binding.CurrentPatchVersion,
		Patch:   make(entities.Patch, 0, 1+len(remoteBaseRoots)+len(remoteBaseFiles)),
	}

	c.remoteBasePatch.Patch = append(c.remoteBasePatch.Patch, &entities.PatchEntry{
		Path: path.Join(c.outDir, "soong/.temp/sbox"),
		Hash: entities.EmptyDirHash,
		Mode: entities.FileModeDir,
	})

	for _, root := range remoteBaseRoots {
		root = strings.ReplaceAll(root, "$OUT_DIR", c.outDir)
		matches, err := filepath.Glob(root)
		if err != nil {
			continue
		}
		for _, dir := range matches {
			if err := c.addRemoteRoot(dir); err != nil {
				return err
			}
		}
	}

	files := make([]string, 0, len(remoteBaseFiles))
	for _, f := range remoteBaseFiles {
		f = strings.ReplaceAll(f, "$OUT_DIR", c.outDir)
		matches, err := filepath.Glob(f)
		if err != nil {
			continue
		}
		files = append(files, matches...)
	}

	filesC := make(chan string, len(files))
	for _, f := range files {
		filesC <- f
	}
	close(filesC)

	if err := c.fetchHashes(filesC); err != nil {
		return err
	}

	for _, f := range files {
		ent := c.hashes.Get(f)
		if ent == nil || ent == notExistsStat {
			continue
		}

		c.remoteBasePatch.Patch = append(c.remoteBasePatch.Patch, &entities.PatchEntry{
			Path: f,
			Hash: ent.Hash,
			Mode: ent.Mode,
			Size: ent.Size,
		})
	}

	ctx := context.Background()

	// TODO: handle out/.path_interposer_origpath
	if err := c.addPathInterposer(ctx, path.Join(c.outDir, ".path_interposer_origpath"), &c.remoteBasePatch.Patch); err != nil {
		return nil
	}
	if err := c.remoteBasePatch.Encode(); err != nil {
		return fmt.Errorf("cannot encode PatchObject: %w", err)
	}

	if err := c.abfs.Store().PutObject(ctx, c.remoteBasePatch.Hash, c.remoteBasePatch.Encoded); err != nil {
		return fmt.Errorf("cannot store remote base PatchObject %s: %w", c.remoteBasePatch.Hash, err)
	}
	fmt.Printf("Remote enabled base=%s\n", c.remoteBasePatch.Hash)
	c.useRemote = true
	return nil
}

func (c *ABFSBackend) setupRemotableRules() {
	if c.config.RemoteBootstrap {
		c.remotableRules["g.bootstrap.compile"] = struct{}{}
	}
	if c.config.RemoteJava {
		c.remotableRules["g.java.javac"] = struct{}{}
		c.remotableRules["g.java.d8"] = struct{}{}
		c.remotableRules["g.java.r8"] = struct{}{}
		c.remotableRules["g.java.turbine"] = struct{}{}
		c.remotableRules["g.java.signapk"] = struct{}{}
		c.remotableRules["g.java.jarjar"] = struct{}{}
		c.remotableRules["g.java.combineJar"] = struct{}{}
		c.remotableRules["g.java.aapt2Compile"] = struct{}{}
	}
	if c.config.RemoteCC {
		c.remotableRules["g.cc.cc"] = struct{}{}
		c.remotableRules["g.cc.ld"] = struct{}{}
	}
	if c.config.RemoteRust {
		c.remotableRules["g.rust.rustc"] = struct{}{}
	}
	if c.config.RemoteKotlin {
		c.remotableRules["g.java.kotlinc"] = struct{}{}
		c.remotableRules["g.java.kaptStubs"] = struct{}{}
	}

	var lock sync.Mutex
	c.graph.Nodes.GoRange(func(obj *graph.Node) bool {
		switch {
		case c.config.RemoteMetalava && strings.HasSuffix(obj.Build.RuleName, ".metalava"):
		case c.config.RemoteMetalava && strings.HasSuffix(obj.Build.RuleName, ".metalava_exportable"):
		default:
			return true
		}

		lock.Lock()
		c.remotableRules[obj.Build.RuleName] = struct{}{}
		lock.Unlock()

		return true
	})
}

func (c *ABFSBackend) remoteOrderOnlyPatch(ctx context.Context, eval *Eval) (entities.Hash, error) {
	ins := eval.step.Node.Build.OrderOnlyInputs
	if len(ins) == 0 {
		return entities.ZeroHash, nil
	}

	patch := make(entities.Patch, 0, len(ins))
	for _, f := range ins {
		if c.orderOnlyAsRegularInput(f) {
			continue
		}
		// Ignore phony order-only inputs.
		if n := c.graph.Targets.Get(f); n != nil && n.Build.Rule.IsPhony() {
			continue
		}
		stat, err := c.lookup(f)
		if err != nil {
			return entities.ZeroHash, err
		}
		if stat == notExistsStat {
			return entities.ZeroHash, entities.NotExistErrf("cannot find order-only input %q", f)
		}
		patch = append(patch, &entities.PatchEntry{
			Path: f,
			Hash: stat.Hash,
			Mode: stat.Mode,
			Size: stat.Size,
		})
	}
	if len(patch) == 0 {
		return entities.ZeroHash, nil
	}

	r := &binding.PatchObject{
		Version: binding.CurrentPatchVersion,
		Patch:   patch,
	}
	if err := r.Encode(); err != nil {
		return entities.ZeroHash, err
	}
	if _, found := c.remotePatches.Load(r.Hash); found {
		return r.Hash, nil
	}

	if err := c.abfs.Store().PutObject(ctx, r.Hash, r.Encoded); err != nil {
		return entities.ZeroHash, err
	}

	c.remotePatches.Store(r.Hash, struct{}{})

	return r.Hash, nil
}

// evalSymlinks resolves a path, following all symlinks,
// and returns the final resolved path, a list of symlinks that were
// encountered and followed, and an error if any.
func evalSymlinks(cwd, path string) (string, []string, error) {
	const maxLinks = 255
	var symlinks []string
	linksFollowed := 0

	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", nil, err
	}
	absPath = filepath.Clean(absPath)

	vol := filepath.VolumeName(absPath)
	p := absPath[len(vol):]

	remainingComps := strings.Split(p, string(os.PathSeparator))

	resolvedPath := vol

	if len(remainingComps) > 0 && remainingComps[0] == "" {
		resolvedPath += string(os.PathSeparator)
		remainingComps = remainingComps[1:]
	}

	i := 0
	for i < len(remainingComps) {
		comp := remainingComps[i]

		if comp == "" || comp == "." {
			i++
			continue
		}
		if comp == ".." {
			resolvedPath = filepath.Dir(resolvedPath)
			i++
			continue
		}

		pathToTest := filepath.Join(resolvedPath, comp)

		fi, err := os.Lstat(pathToTest)
		if err != nil {
			return "", symlinks, err
		}

		if fi.Mode()&os.ModeSymlink != 0 {
			linksFollowed++
			if linksFollowed > maxLinks {
				return "", symlinks, fmt.Errorf("too many symlinks")
			}

			relPath, err := filepath.Rel(cwd, pathToTest)
			if err != nil {
				return "", symlinks, fmt.Errorf("cannot resolve relative-path of %s: %w", pathToTest, err)
			} else if relPath == ".." || strings.HasPrefix(relPath, "../") {
				return "", symlinks, fmt.Errorf("cannot allow parent dir access %s (%q)", pathToTest, relPath)
			} else {
				symlinks = append(symlinks, relPath)
			}

			target, err := os.Readlink(pathToTest)
			if err != nil {
				return "", symlinks, err
			}

			if !filepath.IsAbs(target) {
				target = filepath.Join(resolvedPath, target)
			}
			target = filepath.Clean(target)

			targetVol := filepath.VolumeName(target)
			targetPath := target[len(targetVol):]

			resolvedPath = targetVol
			newComps := strings.Split(targetPath, string(os.PathSeparator))

			if len(newComps) > 0 && newComps[0] == "" {
				resolvedPath += string(os.PathSeparator)
				newComps = newComps[1:]
			}

			remainingComps = append(newComps, remainingComps[i+1:]...)
			i = 0
		} else {
			resolvedPath = pathToTest
			i++
		}
	}
	absResult := filepath.Clean(resolvedPath)
	relPath, err := filepath.Rel(cwd, absResult)
	if err != nil {
		return "", symlinks, fmt.Errorf("cannot resolve relative-path of %s: %w", absResult, err)
	} else if relPath == ".." || strings.HasPrefix(relPath, "../") {
		return "", symlinks, fmt.Errorf("cannot allow parent dir access %s (%q)", absResult, relPath)
	}
	return relPath, symlinks, nil
}

func resolveDeps(targets []string, depsList []*parse.DepsRecord) ([]string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("cannot get cwd: %w", err)
	}

	if len(depsList) == 0 {
		return nil, nil
	}

	targetsSet := make(map[string]struct{}, len(targets))
	for _, t := range targets {
		targetsSet[t] = struct{}{}
	}
	set := make(map[string]struct{})
	result := make([]string, 0, len(targets))
	add := func(d string) {
		if _, ok := set[d]; ok {
			return
		}
		set[d] = struct{}{}
		result = append(result, d)
	}
	for _, deps := range depsList {
		if _, ok := targetsSet[deps.Target]; !ok {
			continue
		}
		for _, d := range deps.Deps {
			//FIXME concurrent
			d, syms, err := evalSymlinks(cwd, d)
			if err != nil {
				return nil, err
			}
			add(d)
			for _, s := range syms {
				add(s)
			}
		}
		return result, nil
	}
	return nil, nil
}

func (c *ABFSBackend) detectCCDeps(eval *Eval) (entities.Patch, error) {
	cmd := eval.step.Command()
	cmd = strings.TrimPrefix(cmd, "PWD=/proc/self/cwd ")
	args := strings.SplitN(cmd, " ", 2)
	dir, base := path.Split(args[0])
	if base != "clang" && base != "clang++" {
		return nil, fmt.Errorf("bad file %s", args[0])
	}
	cmd = fmt.Sprintf("%s -format=make -- %s", path.Join(dir, "clang-scan-deps"), cmd)

	// FIXME: Run this in an NSJail enviroment.
	scan := exec.Command("bash", "-c", cmd)
	out, err := scan.Output()
	if err != nil {
		c.log.Warnf("failed to run clang-scan-deps: %s\n%s", cmd, out)
		return nil, err
	}

	depsList, err := parse.ParseDeps(out)
	if err != nil {
		return nil, fmt.Errorf("failed to parse clang-scan-deps content: %v: %s", err, out)
	}

	deps, err := resolveDeps(eval.step.Node.Targets, depsList)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve clang-scan-deps deps: %v", err)
	}

	//FIXME parallel
	patch := make(entities.Patch, 0, len(deps))
	for _, d := range deps {
		stat, err := c.lookup(d)
		if err != nil {
			return nil, err
		}
		if stat == notExistsStat {
			continue
		}

		patch = append(patch, &entities.PatchEntry{
			Path: d,
			Hash: stat.Hash,
			Mode: stat.Mode,
			Size: stat.Size,
		})
	}
	return patch, nil
}

func (c *ABFSBackend) remoteDepsPatch(eval *Eval) (entities.Patch, error) {
	switch eval.step.Node.Build.RuleName {
	case "g.cc.cc":
		return c.detectCCDeps(eval)
	default:
		return nil, nil
	}
}

func (c *ABFSBackend) remoteDepsPatchHash(ctx context.Context, eval *Eval) (entities.Hash, error) {
	patch, err := c.remoteDepsPatch(eval)
	if err != nil || len(patch) == 0 {
		return entities.ZeroHash, err
	}

	r := &binding.PatchObject{
		Version: binding.CurrentPatchVersion,
		Patch:   patch,
	}
	if err := r.Encode(); err != nil {
		return entities.ZeroHash, err
	}

	if err := c.abfs.Store().PutObject(ctx, r.Hash, r.Encoded); err != nil {
		return entities.ZeroHash, err
	}

	return r.Hash, nil
}

func (c *ABFSBackend) runRemote(ctx context.Context, eval *Eval) (*binding.EvalResponse, error) {
	if err := c.abfs.Store().PutObject(ctx, eval.req.Hash, eval.req.Encoded); err != nil {
		return nil, err
	}

	reqAndPatches := []entities.Hash{eval.req.Hash, c.remoteBasePatch.Hash}

	orderOnlyPatch, err := c.remoteOrderOnlyPatch(ctx, eval)
	if err != nil {
		return nil, err
	}
	if !orderOnlyPatch.IsZero() {
		reqAndPatches = append(reqAndPatches, orderOnlyPatch)
	}

	if eval.step.Node.DepFile() != "" {
		depsPatch, err := c.remoteDepsPatchHash(ctx, eval)
		if err != nil {
			return nil, fmt.Errorf("failed to prepare remote deps patch for %s: %w", eval.step, err)
		} else if !depsPatch.IsZero() {
			reqAndPatches = append(reqAndPatches, depsPatch)
		}
	}

	eval.remotePatches = reqAndPatches[1:]

	if err := c.abfs.WaitFor(remoteReqPushTimeout, 1, reqAndPatches); err != nil {
		return nil, fmt.Errorf("failed to upload request and patches %v: %w", reqAndPatches, err)
	}

	respHash, err := c.abfs.Execute(eval.req.Hash, eval.remotePatches, true, c.config.PrefetchInputs)
	if err != nil {
		// we want the patches included in the log so we can debug/replicate the issue
		return nil, fmt.Errorf("failed to execute req %v with patches %v on remote: %v", eval.req.Hash, eval.remotePatches, err)
	}

	return binding.FetchObjectWarnOnRetry[binding.EvalResponse](ctx, c.abfs.Store(), respHash, remoteRespFetchTimeout)
}

func (c *ABFSBackend) execRemote(ctx context.Context, eval *Eval) (*binding.EvalResponse, error) {
	resp, err := c.runRemote(ctx, eval)
	if err != nil {
		return resp, err
	}
	if resp.ExitCode == 0 {
		if err := c.abfs.ApplyPatch(nil, resp.WritePatch, resp.Deletes, c.chroot); err != nil {
			// TODO invalidate the hashes in case apply-patch has failed in the middle of updates
			return nil, err
		}
	}
	return resp, nil
}
