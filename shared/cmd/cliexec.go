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

package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/pflag"
	"go.coder.com/cli"
	"golang.org/x/sys/unix"

	"github.com/google/abfs/shared/cmd/clierr"
	"github.com/google/abfs/shared/metrics"
)

type cliCommand struct {
	abfsCmd ABFSCmd
	spec    CmdSpec
}

var _ cli.Command = (*cliCommand)(nil)

func (c *cliCommand) Spec() cli.CommandSpec {
	c.spec = c.abfsCmd.Spec()
	return cli.CommandSpec{
		Name:    c.spec.Name,
		Aliases: c.spec.Aliases,
		Usage:   c.spec.Usage,
		Desc:    c.spec.Desc,
		RawArgs: c.spec.RawArgs,
		Hidden:  c.spec.Hidden,
	}
}

func (c *cliCommand) Subcommands() []cli.Command {
	var clis []cli.Command
	for _, cmd := range c.spec.Subcommands {
		clis = append(clis, ABFSCmdToCli(cmd))
	}
	return clis
}

func (c *cliCommand) RegisterFlags(fl *pflag.FlagSet) {
	c.abfsCmd.RegisterFlags(fl)
}

func (c *cliCommand) Run(fl *pflag.FlagSet) {
	exe := &CliExec{
		cliCommand: c,
	}
	exe.Run(fl)
}

type CliExec struct {
	*cliCommand
	providers           []Provider
	configProvider      *ConfigProvider
	storeProvider       *StoreProvider
	cachemanProvider    *CachemanProvider
	abfsClientProvider  *ABFSClientProvider
	mountClientProvider *MountClientProvider
}

type daemonHolder interface {
	CloseDaemon() error
}

func Printf(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if isTerminal(os.Stdout) {
		msg = formatError(msg)
	}
	fmt.Printf("%s", msg)
}

func (c *CliExec) Fatal(err error) {
	var clierr *clierr.CLIError
	if errors.As(err, &clierr) {
		holder, isDaemon := c.abfsCmd.(daemonHolder)
		if errStr := clierr.Error(); errStr != "" {
			if isDaemon || isTerminal(os.Stderr) {
				errStr = formatError(errStr)
			}
			fmt.Fprintf(os.Stderr, "%s\n", errStr)
		}
		if isDaemon {
			_ = holder.CloseDaemon()
		}
		os.Exit(clierr.ExitCode())
	}
	_ = log.Output(syscall.Stderr, err.Error())
	if holder, ok := c.abfsCmd.(daemonHolder); ok {
		_ = holder.CloseDaemon()
	}
	os.Exit(1)
}

func formatError(msg string) string {
	var builder strings.Builder
	inBackticks := false
	for _, r := range msg {
		if r == '`' {
			if inBackticks {
				builder.WriteString("\033[22m")
				inBackticks = false
			} else {
				builder.WriteString("\033[1m")
				inBackticks = true
			}
		} else {
			builder.WriteRune(r)
		}
	}
	if inBackticks {
		builder.WriteString("\033[22m")
	}
	return builder.String()
}

func isTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	return err == nil
}

func (c *CliExec) signalWrap(ctx context.Context, f func(ctx context.Context) error) error {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)
	signal.Notify(sigChan, syscall.SIGTERM)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	closed := false

	go func() {
		// On the first Interrupt (Ctrl+C) try a clean exit
		select {
		case <-sigChan:
			log.Println("Received a shutdown signal, attempting to cleanly exit...")
			go func() {
				// On the second Interrupt (Ctrl+C) try os.Exit()
				<-sigChan
				fmt.Fprintf(os.Stderr, "Process didn't exit, trying to exit forcefully\n")
				os.Exit(1)
			}()
			closed = true
			cancel()
		case <-ctx.Done():
			return
		}
	}()

	err := f(ctx)
	if err != nil && !closed {
		return err
	}
	return nil
}

func SetEmbedded[T Provider](o any, newObj func() (T, error)) error {
	var setProvider func(reflect.Value) bool
	var ret T
	var retErr error
	setProvider = func(v reflect.Value) bool {
		if v.Kind() == reflect.Pointer && v.IsNil() {
			return false
		}

		if v.Kind() == reflect.Pointer {
			v = v.Elem()
		}
		if v.Kind() != reflect.Struct {
			return false
		}

		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			fieldVal := v.Field(i)

			if f.Type == reflect.TypeFor[T]() {
				if fieldVal.CanSet() {
					ret, retErr = newObj()
					if retErr != nil {
						return false
					}
					fieldVal.Set(reflect.ValueOf(ret))
					return true
				}
			}

			if f.Anonymous {
				if setProvider(fieldVal) {
					return true
				}
			}
		}
		return false
	}

	setProvider(reflect.ValueOf(o))
	return retErr
}

func (c *CliExec) newConfigProvider() (*ConfigProvider, error) {
	return newConfigProvider(c)
}

func (c *CliExec) newStoreProvider() (*StoreProvider, error) {
	return newStoreProvider(c)
}

func (c *CliExec) newCachemanProvider() (*CachemanProvider, error) {
	return newCachemanProvider(c)
}

func (c *CliExec) newABFSClientProvider() (*ABFSClientProvider, error) {
	return newABFSClientProvider(c)
}

func (c *CliExec) newMountClientProvider() (*MountClientProvider, error) {
	return newMountClientProvider(c)
}

func (c *CliExec) setProviders(o any) error {
	configProvider, err := c.newConfigProvider()
	if err != nil {
		return err
	}

	if err := SetEmbedded(o, c.newConfigProvider); err != nil {
		return err
	}
	if err := SetEmbedded(o, c.newStoreProvider); err != nil {
		return err
	}
	if err := SetEmbedded(o, c.newCachemanProvider); err != nil {
		return err
	}
	if err := SetEmbedded(o, c.newMountClientProvider); err != nil {
		return err
	}

	// Only initialize ABFSClientProvider if RBE is not enabled as CAS.
	if !configProvider.Config.RBEAsCAS {
		return SetEmbedded(o, c.newABFSClientProvider)
	}
	return nil
}

func (c *CliExec) loadProviders() error {
	if err := c.setProviders(c.abfsCmd); err != nil {
		return err
	}

	for _, o := range c.spec.ProviderInjects {
		if err := c.setProviders(o); err != nil {
			return err
		}
	}
	return nil
}

func (c *CliExec) Run(fl *pflag.FlagSet) {
	var exitErr error
	defer func() {
		if r := recover(); r != nil {
			if clierr, ok := r.(*clierr.CLIError); ok {
				c.Fatal(clierr)
			}
			panic(r)
		}
		if exitErr != nil {
			c.Fatal(exitErr)
		} else {
			if holder, ok := c.abfsCmd.(daemonHolder); ok {
				_ = holder.CloseDaemon()
			}
		}
	}()
	exitErr = c.signalWrap(context.Background(), func(ctx context.Context) error {
		err := c.loadProviders()
		if err != nil {
			return err
		}

		lazyProvider := metrics.NewLazyProvider(func() metrics.Provider {
			configProvider, _ := c.newConfigProvider()
			if configProvider.Instance != nil {
				metrics.SetInstanceName(configProvider.Instance.Name)
			}

			// Flag daemon services to group by instance and prevent metrics overwrite.
			if c.spec.Daemon {
				metrics.SetDaemon(true)
			}

			providerName := configProvider.Config.MetricsProvider
			if providerName == "" {
				return metrics.NewNoopProvider()
			}
			return metrics.NewProvider(providerName, configProvider.Config.RecordClientMetrics)
		})
		metrics.SetProvider(lazyProvider)

		defer func() {
			flushCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := lazyProvider.Close(flushCtx); err != nil {
				log.Printf("Failed to flush metrics on exit: %v\n", err)
			}
		}()

		for i := len(c.providers) - 1; i >= 0; i-- {
			provider := c.providers[i]
			if provider == nil {
				continue
			}
			defer func() {
				if err := provider.Close(); err != nil {
					fmt.Fprintf(os.Stderr, "Error closing provider: %v\n", err)
				}
			}()
		}

		return c.abfsCmd.Exec(ctx, fl)
	})
}

func ABFSCmdToCli(cmd ABFSCmd) cli.Command {
	return &cliCommand{abfsCmd: cmd}
}
