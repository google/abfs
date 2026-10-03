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

package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	version "github.com/hashicorp/go-version"
	"github.com/spf13/pflag"
	"golang.org/x/sys/unix"

	"github.com/google/abfs/daemon"
	cachemanclient "github.com/google/abfs/shared/cacheman/client"
	"github.com/google/abfs/shared/cmd"
	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/cmd/root"
	"github.com/google/abfs/shared/entities"
	"github.com/google/abfs/shared/instance"
	abfsversion "github.com/google/abfs/shared/version"

	abfsclient "github.com/google/abfs/shared/remote/grpc/client"
)

// CheckName identifies an individual health check or a group of health checks in Doctor.
type CheckName string

const (
	// Individual health checks.
	CheckCachemanStatus        CheckName = "cacheman-status"
	CheckCachemanCompatibility CheckName = "cacheman-compatibility"
	CheckCachemanRemote        CheckName = "cacheman-remote"
	CheckCASFSModule           CheckName = "casfs-module"
	CheckCASFSCompatibility    CheckName = "casfs-compatibility"
	CheckCASFSUpstream         CheckName = "casfs-upstream"
	CheckServer                CheckName = "server"

	// Group aliases covering multiple related checks.
	CheckInterfaceVersion CheckName = "interface-version"
	CheckRemote           CheckName = "remote"
	CheckCacheman         CheckName = "cacheman"
	CheckCASFS            CheckName = "casfs"
)

var validCheckNames = []CheckName{
	CheckInterfaceVersion,
	CheckCachemanStatus,
	CheckCachemanCompatibility,
	CheckCachemanRemote,
	CheckCASFSModule,
	CheckCASFSCompatibility,
	CheckCASFSUpstream,
	CheckServer,
	CheckRemote,
	CheckCacheman,
	CheckCASFS,
}

// ValidCheckNames returns the list of supported check names that can be skipped.
func ValidCheckNames() []CheckName {
	out := make([]CheckName, len(validCheckNames))
	copy(out, validCheckNames)
	return out
}

// ParseCheckName validates and normalizes a check name string.
func ParseCheckName(raw string) (CheckName, error) {
	normalized := CheckName(strings.ToLower(strings.TrimSpace(raw)))
	for _, valid := range validCheckNames {
		if normalized == valid {
			return valid, nil
		}
	}
	names := make([]string, len(validCheckNames))
	for i, v := range validCheckNames {
		names[i] = string(v)
	}
	return "", fmt.Errorf("unknown doctor check %q (valid checks: %s)", raw, strings.Join(names, ", "))
}

type doctorCmd struct {
	*root.RootCmd
	*cmd.ConfigProvider
	verbose bool
	skip    []string
}

func NewCmd(rootCmd *root.RootCmd) *doctorCmd {
	return &doctorCmd{RootCmd: rootCmd}
}

// https://github.com/mattn/go-isatty
func isTerminal() bool {
	_, err := unix.IoctlGetTermios(int(os.Stdout.Fd()), unix.TCGETS)
	return err == nil
}

type doctorMessage struct {
	msg strings.Builder
}

func NewDoctorMessage() *doctorMessage {
	return &doctorMessage{}
}

func (d *doctorMessage) Append(format string, args ...any) *doctorMessage {
	fmt.Fprintf(&d.msg, format, args...)
	return d
}

func (d *doctorMessage) AppendString(s string) *doctorMessage {
	d.msg.WriteString(s)
	return d
}

func (d *doctorMessage) AppendError(format string, args ...any) *doctorMessage {
	if isTerminal() {
		d.msg.WriteString("\033[31m")
		defer d.msg.WriteString("\033[0m")
	}
	d.msg.WriteString("ERROR: ")
	fmt.Fprintf(&d.msg, format, args...)
	return d
}
func (d *doctorMessage) AppendInfo(format string, args ...any) *doctorMessage {
	if isTerminal() {
		d.msg.WriteString("\033[33m")
		defer d.msg.WriteString("\033[0m")
	}
	d.msg.WriteString("INFO: ")
	fmt.Fprintf(&d.msg, format, args...)
	return d
}
func (d *doctorMessage) AppendSuggestion(format string, args ...any) *doctorMessage {
	if isTerminal() {
		d.msg.WriteString("\033[34m")
		defer d.msg.WriteString("\033[0m")
	}
	d.msg.WriteString("SUGGESTION: ")
	fmt.Fprintf(&d.msg, format, args...)
	return d
}

func (d *doctorMessage) AppendSuccess(format string, args ...any) *doctorMessage {
	if isTerminal() {
		d.msg.WriteString("\033[32m")
		defer d.msg.WriteString("\033[0m")
	}
	d.msg.WriteString("SUCCESS: ")
	fmt.Fprintf(&d.msg, format, args...)
	return d
}

func (d *doctorMessage) String() string {
	return d.msg.String()
}

func (c *doctorCmd) Spec() cmd.CmdSpec {
	return cmd.CmdSpec{
		Name:    "doctor",
		Usage:   "[-v] [--skip <check>[,<check>...]]",
		Aliases: []string{},
		Desc:    "Detect environment related issues and suggest fixes.",
	}
}

func (c *doctorCmd) RegisterFlags(fl *pflag.FlagSet) {
	fl.BoolVarP(&c.verbose, "verbose", "v", false, "Display detailed breakdown of health checks and evaluated values")
	fl.StringSliceVarP(
		&c.skip,
		"skip",
		"s",
		nil,
		"Checks to skip (e.g. interface-version, cacheman-status, cacheman-compatibility, cacheman-remote, casfs-module, casfs-compatibility, casfs-upstream, server, remote, cacheman, casfs)",
	)
}

type Doctor struct {
	Instance   *instance.Instance
	Verbose    bool
	SkipChecks []CheckName
	TailLogFn  func() string
}

func NewDoctor(inst *instance.Instance) *Doctor {
	return &Doctor{
		Instance: inst,
		Verbose:  false,
	}
}

// Skip configures the Doctor instance to skip the specified checks and returns the Doctor for chaining.
func (d *Doctor) Skip(checks ...CheckName) *Doctor {
	d.SkipChecks = append(d.SkipChecks, checks...)
	return d
}

// IsSkipped reports whether a specific check should be skipped based on Doctor configuration.
func (d *Doctor) IsSkipped(target CheckName) bool {
	for _, skipped := range d.SkipChecks {
		if skipped == target {
			return true
		}
		switch skipped {
		case CheckInterfaceVersion:
			if target == CheckCachemanCompatibility || target == CheckCASFSCompatibility {
				return true
			}
		case CheckRemote:
			if target == CheckCachemanRemote || target == CheckServer {
				return true
			}
		case CheckCacheman:
			if target == CheckCachemanStatus || target == CheckCachemanCompatibility || target == CheckCachemanRemote {
				return true
			}
		case CheckCASFS:
			if target == CheckCASFSModule || target == CheckCASFSCompatibility || target == CheckCASFSUpstream {
				return true
			}
		case CheckCASFSModule:
			if target == CheckCASFSCompatibility {
				return true
			}
		}
	}
	return false
}

// PreflightCheck runs doctor health checks for CLI commands prior to executing operations.
// It skips execution if running inside a background daemon process.
// Optional CheckName arguments can be provided by callers to skip specific checks (e.g. CheckInterfaceVersion).
func PreflightCheck(inst *instance.Instance, skip ...CheckName) error {
	if daemon.IsDaemon() {
		return nil
	}

	d := NewDoctor(inst).Skip(skip...)
	if err := d.Check(); err != nil {
		return fmt.Errorf("doctor failed: %w", err)
	}
	return nil
}

func (d *Doctor) logVerbose(format string, args ...any) {
	if !d.Verbose {
		return
	}
	msg := fmt.Sprintf(format, args...)
	if isTerminal() {
		fmt.Printf("\033[36m[CHECK]\033[0m %s\n", msg)
	} else {
		fmt.Printf("[CHECK] %s\n", msg)
	}
}

func (d *Doctor) checkCachemanStatus() error {
	abfsPath, err := exec.LookPath("abfs")
	if err != nil {
		return clierr.CLIErr(
			"%s",
			NewDoctorMessage().
				AppendError("could not locate `abfs` executable in PATH: %v\n", err).
				AppendSuggestion("Ensure abfs is installed and in your PATH.").
				String(),
		)
	}

	args := []string{}
	if d.Instance != nil && d.Instance.Name != "" {
		args = append(args, "--instance", d.Instance.Name)
	}
	args = append(args, "cacheman", "status")
	statusCmd := exec.Command(abfsPath, args...)
	statusOutBytes, statusErr := statusCmd.CombinedOutput()

	if statusErr != nil {
		statusOut := string(statusOutBytes)

		return clierr.CLIErr(
			"%s",
			NewDoctorMessage().
				AppendError("could not detect a running cacheman. `abfs cacheman status` is below:\n").
				AppendString(strings.TrimSpace(statusOut)+"\n").
				AppendSuggestion("Ensure `abfs init` has been run. If cacheman was previously stopped, try `abfs cacheman restart`").
				String(),
		)
	}

	d.logVerbose("Cacheman service: RUNNING (command: %s %s)", abfsPath, strings.Join(args, " "))
	return nil
}

func (d *Doctor) cachemanTailLog() string {
	if d.TailLogFn != nil {
		return d.TailLogFn()
	}

	if d.Instance == nil || d.Instance.Config == nil || d.Instance.Config.LogDir == "" {
		return ""
	}

	abfsPath, err := exec.LookPath("abfs")
	if err != nil {
		return ""
	}

	tailArgs := []string{}
	if d.Instance != nil && d.Instance.Name != "" {
		tailArgs = append(tailArgs, "--instance", d.Instance.Name)
	}
	tailArgs = append(tailArgs, "cacheman", "tail")

	// Capture 500 ms of 'abfs cacheman tail' in a separate process group
	// so that the grandchild 'tail' process that 'cacheman tail' spawns also tears down
	// on process group signal.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, abfsPath, tailArgs...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	out, _ := cmd.CombinedOutput()

	return string(out)
}

func (d *Doctor) checkCachemanCompatibility() error {
	client := cachemanclient.NewHTTPClient(d.Instance.Config.CacheDir)
	impStr, reqStr, err := client.InterfaceVersion()
	if err != nil {
		return clierr.CLIErr(
			"%s",
			NewDoctorMessage().
				AppendError("could not query cacheman interface version: %v\n", err).
				AppendSuggestion("The service may be starting up, stuck, or out of date. Please inspect the log context below:\n").
				AppendString(d.cachemanTailLog()).
				String(),
		)
	}

	cachemanImplementedVer, err := version.NewVersion(impStr)
	if err != nil {
		return err
	}
	cachemanRequiredVer, err := version.NewVersion(reqStr)
	if err != nil {
		return err
	}

	// 1. Cacheman implemented version must be >= ABFS required version
	abfsRequiresCachemanVer, err := version.NewVersion(abfsversion.ABFSCachemanRequiredVersion)
	if err != nil {
		return err
	}
	if cachemanImplementedVer.LessThan(abfsRequiresCachemanVer) {
		return clierr.CLIErr(
			"%s",
			NewDoctorMessage().
				AppendError("Cacheman implemented interface version is older than ABFS required version\n").
				AppendSuggestion("Please update or restart Cacheman.").
				String(),
		)
	}

	// 2. ABFS implemented version must be >= Cacheman required version
	abfsImplementsCachemanVer, err := version.NewVersion(abfsversion.ABFSCachemanImplementedVersion)
	if err != nil {
		return err
	}
	if abfsImplementsCachemanVer.LessThan(cachemanRequiredVer) {
		return clierr.CLIErr(
			"%s",
			NewDoctorMessage().
				AppendError("ABFS client implemented interface version is older than Cacheman required version\n").
				AppendSuggestion("Please upgrade your ABFS client.").
				String(),
		)
	}

	d.logVerbose("Cacheman interface compatibility: OK (Cacheman: impl=%s req=%s | ABFS: impl=%s req=%s)",
		impStr, reqStr, abfsversion.ABFSCachemanImplementedVersion, abfsversion.ABFSCachemanRequiredVersion)
	return nil
}

func (d *Doctor) checkCASFSModule() error {
	if d.Instance.Config.FSType != instance.CASFSTypeKmod {
		d.logVerbose("CASFS module: SKIPPED (filesystem type is %q)", d.Instance.Config.FSType)
		return nil
	}

	if _, err := os.Stat("/sys/module/casfs"); os.IsNotExist(err) {
		return clierr.CLIErr(
			"%s",
			NewDoctorMessage().
				AppendError("CASFS module is not inserted\n").
				AppendSuggestion("\n"+
					"  *. If the module is registered with depmod, try to insert with `sudo modprobe casfs`\n"+
					"  *. If you are compiling CASFS from source, try to insert with `sudo insmod <path to casfs.ko>`").
				String(),
		)
	}

	d.logVerbose("CASFS module: LOADED (/sys/module/casfs present)")
	return nil
}

func (d *Doctor) checkCASFSCompatibility() error {
	if d.Instance.Config.FSType != instance.CASFSTypeKmod {
		d.logVerbose("CASFS compatibility: SKIPPED (filesystem type is %q)", d.Instance.Config.FSType)
		return nil
	}

	impBytes, err := os.ReadFile("/sys/module/casfs/parameters/implemented_interface_version")
	if err != nil {
		return clierr.CLIErr(
			"%s",
			NewDoctorMessage().
				AppendError("CASFS module does not expose implemented interface version\n").
				AppendSuggestion("Please update the CASFS kernel module.").
				String(),
		)
	}

	reqBytes, err := os.ReadFile("/sys/module/casfs/parameters/required_interface_version")
	if err != nil {
		return clierr.CLIErr(
			"%s",
			NewDoctorMessage().
				AppendError("CASFS module does not expose required interface version\n").
				AppendSuggestion("Please update the CASFS kernel module.").
				String(),
		)
	}

	impStr := strings.TrimSpace(string(impBytes))
	reqStr := strings.TrimSpace(string(reqBytes))

	casfsImplementedVer, err := version.NewVersion(impStr)
	if err != nil {
		return err
	}
	casfsRequiredVer, err := version.NewVersion(reqStr)
	if err != nil {
		return err
	}

	// 1. CASFS implemented version must be >= ABFS required version
	abfsRequiresCASFSVer, err := version.NewVersion(abfsversion.ABFSCASFSRequiredVersion)
	if err != nil {
		return err
	}
	if casfsImplementedVer.LessThan(abfsRequiresCASFSVer) {
		return clierr.CLIErr(
			"%s",
			NewDoctorMessage().
				AppendError("CASFS implemented interface version is older than ABFS required version\n").
				AppendSuggestion("Please update your CASFS kernel module.").
				String(),
		)
	}

	// 2. ABFS implemented version must be >= CASFS required version
	abfsImplementsCASFSVer, err := version.NewVersion(abfsversion.ABFSCASFSImplementedVersion)
	if err != nil {
		return err
	}
	if abfsImplementsCASFSVer.LessThan(casfsRequiredVer) {
		return clierr.CLIErr(
			"%s",
			NewDoctorMessage().
				AppendError("ABFS client implemented interface version is older than CASFS required version\n").
				AppendSuggestion("Please upgrade your ABFS client.").
				String(),
		)
	}

	d.logVerbose("CASFS kmod compatibility: OK (CASFS: impl=%s req=%s | ABFS: impl=%s req=%s)",
		impStr, reqStr, abfsversion.ABFSCASFSImplementedVersion, abfsversion.ABFSCASFSRequiredVersion)
	return nil
}
func isCASFSStatusValid(status string) bool {
	switch status {
	case "connected (0)", "local (0)":
		return true
	default:
		return false
	}
}

func (d *Doctor) checkCASFSUpstreamStatus() error {
	baseDir := "/sys/fs/casfs"

	// If the sysfs directory doesn't exist or isn't accessible, don't fail.
	if _, err := os.Stat(baseDir); os.IsNotExist(err) {
		d.logVerbose("CASFS upstream status: SKIPPED (/sys/fs/casfs does not exist)")
		return nil
	}

	statusFiles, err := filepath.Glob(filepath.Join(baseDir, "*/upstream_status"))
	if err != nil {
		d.logVerbose("CASFS upstream status: failed to glob upstream_status: %v", err)
		return nil
	}

	if len(statusFiles) == 0 {
		d.logVerbose("CASFS upstream status: NONE (no instances found in %s)", baseDir)
		return nil
	}

	var badStatuses []string
	for _, sf := range statusFiles {
		instName := filepath.Base(filepath.Dir(sf))
		content, readErr := os.ReadFile(sf)
		if readErr != nil {
			// If permissions prevent reading sysfs in a restricted sandbox,
			// log it in verbose mode rather than hard-failing the doctor check.
			d.logVerbose("CASFS instance %q upstream status: unreadable (%v)", instName, readErr)
			continue
		}

		status := strings.TrimSpace(string(content))
		if !isCASFSStatusValid(status) {
			badStatuses = append(badStatuses, fmt.Sprintf("- %s: %q (expected: %q or %q)", instName, status, "connected (0)", "local (0)"))
		} else {
			d.logVerbose("CASFS instance %q upstream status: %s", instName, status)
		}
	}

	if len(badStatuses) > 0 {
		msg := NewDoctorMessage().
			AppendInfo("One or more CASFS filesystems reported an abnormal upstream status:\n").
			AppendString(strings.Join(badStatuses, "\n") + "\n").
			AppendSuggestion("Inspect the connection to Cacheman or credentials.")

		// Always print the info advisory when an abnormal status is detected
		fmt.Print(msg.String())
	}

	return nil
}

func (d *Doctor) healthCheckServer() error {
	if d.Instance.Config.LocalOnly {
		d.logVerbose("Remote server reachability: SKIPPED (LocalOnly mode enabled)")
		return nil
	}

	servers := d.Instance.Config.EffectiveRemoteServers()
	client, err := abfsclient.NewABFSClient(&abfsclient.ABFSClientOpts{
		Servers:  servers,
		AuthInfo: d.Instance.Config.GetAuthInfo(),
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = client.Headers(ctx, []entities.Hash{entities.EmptyFileHash})
	if err != nil {
		return err
	}

	d.logVerbose("Remote server reachability: OK (probed empty hash against %v)", servers)
	return nil
}

func (d *Doctor) checkCachemanRemoteOK() error {
	if d.Instance == nil || d.Instance.Config == nil || d.Instance.Config.CacheDir == "" {
		return nil
	}
	if d.Instance.Config.LocalOnly {
		d.logVerbose("Cacheman remote reachability: SKIPPED (LocalOnly mode enabled)")
		return nil
	}

	client := cachemanclient.NewHTTPClient(d.Instance.Config.CacheDir)
	if err := client.RemoteOK(); err != nil {
		msg := NewDoctorMessage()
		errStr := err.Error()

		msg.AppendError("Cacheman reported a remote connection failure:\n%s\n", errStr).
			AppendSuggestion("Check network connectivity to remote endpoints or verify cacheman configuration. If cacheman was recently restarted, try again shortly.\n")

		if tailLog := d.cachemanTailLog(); strings.TrimSpace(tailLog) != "" {
			msg.AppendString("\nPlease inspect the log context below:\n").
				AppendString(tailLog + "\n")
		}

		return clierr.CLIErr("%s", strings.TrimRight(msg.String(), "\n"))
	}

	d.logVerbose("Cacheman remote reachability: OK (verified via cacheman remote-ok)")
	return nil
}

func (d *Doctor) Check() error {
	if d.IsSkipped(CheckCachemanStatus) {
		d.logVerbose("Cacheman service: SKIPPED (skipped via configuration)")
	} else if err := d.checkCachemanStatus(); err != nil {
		return err
	}

	if d.IsSkipped(CheckCachemanCompatibility) {
		d.logVerbose("Cacheman interface compatibility: SKIPPED (skipped via configuration)")
	} else if err := d.checkCachemanCompatibility(); err != nil {
		return err
	}

	if d.IsSkipped(CheckCachemanRemote) {
		d.logVerbose("Cacheman remote reachability: SKIPPED (skipped via configuration)")
	} else if err := d.checkCachemanRemoteOK(); err != nil {
		return err
	}

	if d.IsSkipped(CheckCASFSModule) {
		d.logVerbose("CASFS module: SKIPPED (skipped via configuration)")
	} else if err := d.checkCASFSModule(); err != nil {
		return err
	}

	if d.IsSkipped(CheckCASFSCompatibility) {
		d.logVerbose("CASFS kmod compatibility: SKIPPED (skipped via configuration)")
	} else if err := d.checkCASFSCompatibility(); err != nil {
		return err
	}

	if d.IsSkipped(CheckCASFSUpstream) {
		d.logVerbose("CASFS upstream status: SKIPPED (skipped via configuration)")
	} else if err := d.checkCASFSUpstreamStatus(); err != nil {
		return err
	}

	if d.IsSkipped(CheckServer) {
		d.logVerbose("Remote server reachability: SKIPPED (skipped via configuration)")
	} else if err := d.healthCheckServer(); err != nil {
		return clierr.CLIErr(
			"%s",
			NewDoctorMessage().
				AppendError("direct connection to remote server failed: %v\n", err).
				AppendSuggestion("Cacheman remote check passed, but direct connection from CLI failed. Please confirm direct reachability to remote servers.").
				String(),
		)
	}

	return nil
}

func (c *doctorCmd) Exec(ctx context.Context, fl *pflag.FlagSet) error {
	doctor := NewDoctor(c.Instance)
	doctor.Verbose = c.verbose

	for _, rawSkip := range c.skip {
		checkName, err := ParseCheckName(rawSkip)
		if err != nil {
			return clierr.CLIErr("%s", NewDoctorMessage().AppendError("%v\n", err).String())
		}
		doctor.Skip(checkName)
	}

	if err := doctor.Check(); err != nil {
		return err
	}
	fmt.Println(NewDoctorMessage().AppendSuccess("Health checks passed!").String())
	return nil
}
