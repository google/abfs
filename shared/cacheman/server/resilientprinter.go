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

package server

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/google/abfs/shared/entities"
)

func dateTimePrefix() []byte {
	return []byte(time.Now().Format(time.DateTime) + " ")
}

// ResilientPrinter writes a file at a configured path and will re-create the
// file when it is unlinked.
type ResilientPrinter struct {
	path    string
	file    *os.File
	watcher *fsnotify.Watcher

	// signal exit to the watch goroutine
	exit chan struct{}
	wg   sync.WaitGroup

	closeOnce sync.Once
	closeErr  error
}

func NewResilientPrinter(path string) (*ResilientPrinter, error) {
	r := &ResilientPrinter{
		path: path,
		exit: make(chan struct{}),
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	r.watcher = watcher

	err = r.createFile()
	if err != nil {
		_ = r.Close()
		return nil, err
	}

	if err := r.watcher.Add(r.path); err != nil {
		_ = r.Close()
		return nil, err
	}

	r.wg.Add(1)
	go r.watchFile()

	return r, nil
}

// Update the existing file pointer atomically before closing the
// previous file. Writers either write to old or new file, but not a bad
// fd.
func (r *ResilientPrinter) createFile() (retErr error) {
	prevFile := r.file
	defer func() {
		if prevFile != nil {
			entities.CloseErrCheck(prevFile, &retErr)
		}
	}()

	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	r.file = f

	return nil
}

func (r *ResilientPrinter) Close() error {
	r.closeOnce.Do(func() {
		// Signal for the watch goroutine to exit
		if r.exit != nil {
			close(r.exit)
		}
		var err error
		if r.watcher != nil {
			err = r.watcher.Close()
		}
		if r.file != nil {
			err = errors.Join(err, r.file.Close())
		}
		// Wait for the watch goroutine to exit
		r.wg.Wait()
		r.watcher = nil
		r.file = nil
		r.closeErr = err
	})
	return r.closeErr
}

func (r *ResilientPrinter) watchFile() {
	defer r.wg.Done()

	for {
		select {
		case <-r.exit:
			return
		case event, ok := <-r.watcher.Events:
			if !ok {
				return
			}

			if event.Has(fsnotify.Remove) || event.Has(fsnotify.Chmod) || event.Has(fsnotify.Rename) {
				_, statErr := os.Stat(r.path)
				if errors.Is(statErr, fs.ErrNotExist) {
					select {
					case <-r.exit:
						return
					default:
					}

					if err := r.createFile(); err != nil {
						log.Printf("ResilientPrinter: Unable to recreate log file %s: %v\n", r.path, err)
						return
					}

					_ = r.watcher.Remove(r.path)
					if err := r.watcher.Add(r.path); err != nil {
						log.Printf("ResilientPrinter: Cannot add watch for %s: %v\n", r.path, err)
						return
					}
				}
			}
		case _, ok := <-r.watcher.Errors:
			if !ok {
				return
			}
		}
	}
}

func (r *ResilientPrinter) Printf(format string, a ...any) {
	msg := string(fmt.Appendf(dateTimePrefix(), format, a...))
	if _, err := fmt.Fprint(r.file, msg); err != nil {
		log.Printf("Failed to print log: %s: %v", msg, err)
	}
}

func (r *ResilientPrinter) Println(a ...any) {
	msg := string(fmt.Appendln(dateTimePrefix(), a...))
	if _, err := fmt.Fprint(r.file, msg); err != nil {
		log.Printf("Failed to print log: %s: %v", msg, err)
	}
}

func (r *ResilientPrinter) Fatalf(format string, a ...any) {
	msg := string(fmt.Appendf(dateTimePrefix(), format, a...))
	if _, err := fmt.Fprint(r.file, msg); err != nil {
		log.Printf("Failed to print log: %s: %v", msg, err)
	}
	os.Exit(1)
}

func (r *ResilientPrinter) Fatal(a ...any) {
	msg := string(fmt.Append(dateTimePrefix(), a...))
	if _, err := fmt.Fprint(r.file, msg); err != nil {
		log.Printf("Failed to print log: %s: %v", msg, err)
	}
	os.Exit(1)
}

type DefaultPrinter struct {
}

func (d *DefaultPrinter) Printf(format string, a ...any) {
	fmt.Print(string(fmt.Appendf(dateTimePrefix(), format, a...)))
}

func (d *DefaultPrinter) Println(a ...any) {
	fmt.Print(string(fmt.Appendln(dateTimePrefix(), a...)))
}

func (d *DefaultPrinter) Fatalf(format string, a ...any) {
	fmt.Print(string(fmt.Appendf(dateTimePrefix(), format, a...)))
	os.Exit(1)
}

func (d *DefaultPrinter) Fatal(a ...any) {
	fmt.Print(string(fmt.Append(dateTimePrefix(), a...)))
	os.Exit(1)
}

func (d *DefaultPrinter) Close() error {
	return nil
}

func PrinterOrDefault(printer Printer) Printer {
	if printer == nil {
		return &DefaultPrinter{}
	}

	return printer
}
