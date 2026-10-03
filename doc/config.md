# ABFS Configuration System

ABFS uses a multi-tiered configuration system to manage settings for local instances. There are different sources that contribute to the final configuration that the client uses. These sources in order of precedence are (from lowest to highest):

* Default configuration
* Remote config overrides
* Local config file
* Command line flags

## Core Structures
All of the client configuration is defined in `shared/instance/configdef.go`:

*   **`Config`**: The standard configuration struct containing all settings (e.g., `CacheDir`, `RemoteServers`, `FSType`, etc.).
*   **`ConfigOptional`**: A mirror of `Config` where every field is a pointer. This distinction is crucial:
    *   A zero-value in `Config` (like `""` or `false`) might mean "use default" or be a valid setting.
    *   In `ConfigOptional`, a `nil` pointer means "unset/ignore", while a non-nil pointer means "explicitly set to this value" (even if it's the zero value of the type).

The client uses `Config` to represent the final configuration after all the overrides have been applied (`ConfigOptional`s).

## Default Configuration
Hard-coded default values for some configuration values. 

These are defined in `shared/instance/default.go`.

## Local Config File
The local config file is a YAML file that is used to configure the client instance. It is located in `~/.abfs/<instance>/config`.

We used to store the final rendered configuration in this file, but with the introduction of `ConfigOptional` it should only include the config items that are different from the default values (Clients can run `abfs config -D -w` to remove the redundant config lines).

Some long running abfs commands (like cacheman), might want to watch the config file for changes and reload it when it changes.

## Command Line Flags
The configuration can be overridden using the command-line flags. The abfs root command accepts most 
of the config items as flags. For example, `abfs --cache-dir=/tmp/cache` will set the cache directory to `/tmp/cache`. It will not change the local config file, but it will be used for the duration of the command itself.

For the config items that are not covered by the command line flags explicitly the `--kv <config>=<value>` flag can be used. If the value is not specified it will unset the config item (i.e. set it to the default value).

## Remote Config Overrides
It is possible to override the local config items from the remote server. This is done by setting the client config overrides in `config:clients/configs` hash-path. The hash path will be resolved to a YAML file which contains a set of client filters and their corresponding config overrides (see `shared/instance/remtoteoverrides.go`).
The clients can be selected based on their:
* Class
* User
* MinVersion
* MaxVersion

To resolve a remote override, the client starts with an empty config and applies matching overrides in order. This allows later overrides to override earlier ones.

Currently the server can override any config values but we should limit what can be overridden.

Cacheman reads the remote config overrides when it starts and watches it for changes. Once it is changed, it will match it against the current client instance and update the config items if necessary.

Cacheman also writes the override config to a read-only config file in the instance directory (e.g. `~/.abfs/<instance>/.remote-config`). ABFS commands read this file and apply the overrides to their configuration. If they want to receive updates when the remote config changes, they can watch this file for changes (see below).

## Watching Config changes
The long running commands that want to receive config change updates can register a callback using `instance.RegisterConfigChange()` and then call `instance.StartWatchingConfigFile()` to start watching the config files for changes.

They will get notified when either the local config file or the remote config is changed.

## `abfs config` command line
The `abfs config` command is a handy tool for interacting and debugging the client config. It can be used to show the effective config, the remote config overrides, or the local config file. It can also be used to watch for changes in the config file and print the effective config when it changes.

Some useful examples:
### See effective current config
Current effective config:
```bash
abfs config
```
You can use `--watch` for it to block and print the config when it changes.

### Set a config item
For example, the following command will set a config value and use `-w` / `--write` to write it to the local config file:

```bash
abfs --fs fuse config -w
```

Or use kv flag:

```bash
abfs --kv concurrent-uploads=200 config -w
```

### View the local config
To see the local config as set in the config file use the `-r` / `as-read` flag:

```bash
abfs config -r
```

### Remove the default values
To remove config items with default values from the output or when written to the config file, use the `-D` / `--no-defaults` flag.

For example, the following command will remove the default values from the config files:

```bash
abfs config -w -D
```


