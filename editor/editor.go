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

package editor

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ExecCommand is used to launch external editor processes and can be overridden in tests.
var ExecCommand = exec.Command

const (
	errorHeaderStart = "# === VALIDATION ERROR (this block is automatically removed) ==="
	errorHeaderEnd   = "# =============================================================="
)

// Resolve returns the user's preferred editor command and initial arguments.
// It checks $VISUAL, $EDITOR, `git config --get core.editor`, and falls back to `vi`.
func Resolve() []string {
	for _, env := range []string{"VISUAL", "EDITOR"} {
		if ed := strings.TrimSpace(os.Getenv(env)); ed != "" {
			return strings.Fields(ed)
		}
	}
	cmd := ExecCommand("git", "config", "--get", "core.editor")
	if output, err := cmd.Output(); err == nil {
		if ed := strings.TrimSpace(string(output)); ed != "" {
			return strings.Fields(ed)
		}
	}
	return []string{"vi"}
}

// IsVim reports whether the given editor binary name belongs to the Vim family
// (vi, vim, nvim, gvim, mvim, vim.basic, vim.tiny, etc.).
func IsVim(bin string) bool {
	base := filepath.Base(bin)
	return base == "vi" || base == "vim" || base == "nvim" || base == "gvim" || base == "mvim" ||
		strings.HasPrefix(base, "vim.")
}

// Open launches the user's preferred interactive editor on filePath, passing any extraArgs before filePath.
func Open(filePath string, extraArgs ...string) error {
	ed := Resolve()
	args := append([]string{}, ed[1:]...)
	args = append(args, extraArgs...)
	args = append(args, filePath)

	cmd := ExecCommand(ed[0], args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// EditOptions configures an interactive file editing session with validation and retry support.
type EditOptions struct {
	// TargetFile is the path to the file to copy before editing and atomically replace upon validation.
	// If TargetFile does not exist yet, editing starts from an empty file.
	TargetFile string

	// TempDir is the directory where temporary editing files are created.
	// If empty, defaults to filepath.Dir(TargetFile) when TargetFile is set, or os.TempDir().
	TempDir string

	// TempPrefix is the prefix used when creating the temporary file.
	TempPrefix string

	// FileExtension is the suffix for the temporary file (e.g. ".yaml"). Defaults to ".tmp".
	FileExtension string

	// RejectedFile is the file path where unapplied changes are saved if the user aborts
	// after a validation failure. If empty and TargetFile is set, defaults to TargetFile + ".rejected".
	RejectedFile string

	// Filetype specifies the syntax filetype (e.g. "yaml") for Vim editors.
	Filetype string

	// VimSpecs specifies optional autocomplete keys/values when the editor is Vim/Neovim.
	VimSpecs []VimKeySpec

	// Validate is called on the temporary file after each edit.
	// If Validate returns a non-nil error, the error is prepended as a comment block at the top
	// of the temporary file and the editor is re-opened so the user can fix the issue.
	Validate func(editedPath string) error

	// Apply is an optional callback to apply the validated temporary file.
	// If nil and TargetFile is set, the validated temporary file is atomically renamed to TargetFile.
	Apply func(validatedPath string) error
}

// EditWithValidation copies TargetFile to a temporary file, opens the user's editor, validates
// the result, and re-opens the editor with inline error comments until validation succeeds or the user aborts.
func EditWithValidation(opts EditOptions) error {
	tempDir := opts.TempDir
	if tempDir == "" {
		if opts.TargetFile != "" {
			tempDir = filepath.Dir(opts.TargetFile)
		} else {
			tempDir = os.TempDir()
		}
	}
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", tempDir, err)
	}

	ext := opts.FileExtension
	if ext == "" {
		ext = ".tmp"
	}
	prefix := opts.TempPrefix
	if prefix == "" && opts.TargetFile != "" {
		prefix = filepath.Base(opts.TargetFile) + "."
	}

	tmpFile, err := os.CreateTemp(tempDir, prefix+"*"+ext)
	if err != nil {
		return fmt.Errorf("failed to create temporary file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		if tmpFile != nil {
			_ = tmpFile.Close()
		}
		_ = os.Remove(tmpPath)
	}()

	perm := os.FileMode(0600)
	if opts.TargetFile != "" {
		if srcFile, err := os.Open(opts.TargetFile); err == nil {
			if stat, err := srcFile.Stat(); err == nil {
				perm = stat.Mode().Perm()
			}
			if _, err := io.Copy(tmpFile, srcFile); err != nil {
				_ = srcFile.Close()
				return fmt.Errorf("failed to copy original file: %w", err)
			}
			_ = srcFile.Close()
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("failed to open original file: %w", err)
		}
	}

	if err := tmpFile.Chmod(perm); err != nil {
		return fmt.Errorf("failed to set permissions on temporary file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temporary file: %w", err)
	}
	tmpFile = nil

	rejectedFile := opts.RejectedFile
	if rejectedFile == "" && opts.TargetFile != "" {
		rejectedFile = opts.TargetFile + ".rejected"
	}

	ed := Resolve()
	var extraArgs []string
	if IsVim(ed[0]) && (opts.Filetype != "" || len(opts.VimSpecs) > 0) {
		if vimScriptPath, cleanup, err := SetupVimSupport(tempDir, opts.Filetype, opts.VimSpecs); err == nil {
			defer cleanup()
			extraArgs = append(extraArgs, "-S", vimScriptPath)
		}
	}

	var lastPromptedBytes []byte
	var lastErr error

	for {
		if err := Open(tmpPath, extraArgs...); err != nil {
			if lastErr != nil {
				saveRejectedFile(tmpPath, rejectedFile)
				return lastErr
			}
			return fmt.Errorf("editor failed: %w", err)
		}

		currentBytes, err := os.ReadFile(tmpPath)
		if err != nil {
			return fmt.Errorf("failed to read temporary file: %w", err)
		}

		// If re-opened after a validation error and the user exited without making changes (:q)
		// or cleared the file, abort the edit session.
		if lastPromptedBytes != nil && (bytes.Equal(currentBytes, lastPromptedBytes) || len(bytes.TrimSpace(currentBytes)) == 0) {
			saveRejectedFile(tmpPath, rejectedFile)
			return lastErr
		}

		if err := StripErrorHeader(tmpPath); err != nil {
			return fmt.Errorf("failed to strip error header: %w", err)
		}

		var valErr error
		if opts.Validate != nil {
			valErr = opts.Validate(tmpPath)
		}
		if valErr == nil {
			if opts.Apply != nil {
				if err := opts.Apply(tmpPath); err != nil {
					return err
				}
			} else if opts.TargetFile != "" {
				if err := os.Rename(tmpPath, opts.TargetFile); err != nil {
					return fmt.Errorf("failed to save edited file: %w", err)
				}
			}
			if rejectedFile != "" {
				_ = os.Remove(rejectedFile)
			}
			return nil
		}

		lastErr = valErr
		promptedBytes, err := PrependErrorHeader(tmpPath, valErr)
		if err != nil {
			return fmt.Errorf("failed to prepend error header: %w", err)
		}
		lastPromptedBytes = promptedBytes
	}
}

// StripErrorHeader removes the automatically generated validation error comment block from filePath if present.
func StripErrorHeader(filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	content := string(data)
	if !strings.HasPrefix(content, errorHeaderStart) {
		return nil
	}
	idx := strings.Index(content, errorHeaderEnd)
	if idx >= 0 {
		rest := content[idx+len(errorHeaderEnd):]
		rest = strings.TrimPrefix(rest, "\r\n")
		rest = strings.TrimPrefix(rest, "\n")
		rest = strings.TrimPrefix(rest, "\n")
		return os.WriteFile(filePath, []byte(rest), 0600)
	}
	return nil
}

// PrependErrorHeader adds a commented validation error block to the top of filePath.
func PrependErrorHeader(filePath string, valErr error) ([]byte, error) {
	if err := StripErrorHeader(filePath); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	buf.WriteString(errorHeaderStart + "\n")
	for line := range strings.SplitSeq(valErr.Error(), "\n") {
		buf.WriteString("# " + line + "\n")
	}
	buf.WriteString("#\n")
	buf.WriteString("# Please fix the error(s) below and save (:wq) to retry.\n")
	buf.WriteString("# To abort, exit without saving changes (:q) or quit with an error (:cq).\n")
	buf.WriteString(errorHeaderEnd + "\n\n")
	buf.Write(data)

	newContent := buf.Bytes()
	if err := os.WriteFile(filePath, newContent, 0600); err != nil {
		return nil, err
	}
	return newContent, nil
}

func saveRejectedFile(tmpPath, rejectedFile string) {
	if rejectedFile == "" {
		return
	}
	_ = StripErrorHeader(tmpPath)
	data, err := os.ReadFile(tmpPath)
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		return
	}
	if err := os.WriteFile(rejectedFile, data, 0600); err == nil {
		fmt.Fprintf(os.Stderr, "Unapplied changes saved to %q.\n", rejectedFile)
	}
}
