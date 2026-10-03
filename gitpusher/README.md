# Background

Git-Pusher is an ABFS component which is responsible for pushing the source code from git repositories to the ABFS remote server.

It is a replacement for upload-daemon which was based on a static configuration. Git-Pusher reads the configuration from the ABFS remote server and updates the internal states based on the config.

It supports multiple manifest-based repositories and multiple independent git projects.

# Git-Pusher Architecture

Git-Pusher provides flexibility and scalability by supporting the concept of a pool of pushers. Each pusher is an independent process that can push a subset of projects defined in that pool. The subset is calculated using a consistent hashing algorithm ([`Rendezvous Hashing`](https://en.wikipedia.org/wiki/Rendezvous_hashing)) based on the hash of the project name and the number of pushers in the pool.

# Git-Pusher Configuration

The Git-Pushers is configured via a YAML format on the remote server for the entire pool. It contains the information about the pool size, the list of pushers inside the pool, list of manifests for repos, and the configuration for each project.

The config can be accessed or edited using a hash-path. By default, the pool is `default` and the project is `config`, therefore the config can be edited using:

```shell
abfs edit config:git-pusher/default # Use --add/-a for the first time
```

The format of the config is as follows. The root object is the `GitPusherPoolConfig`. 

Here is a sample config:
```yaml
$ abfs cat config:git-pusher/default

name: default
pushers-count: 3
defaults:
    port: 9998

pushers:
  - name: gerrit-aosp-0
  - name: gerrit-aosp-1
  - name: gerrit-aosp-2

repos:
  - manifest:
      url: https://android.googlesource.com/platform/manifest
      files:
        - branch: main
          file: default.xml

projects:
  - url: github.com/bazil/fuse
  - url: android.googlesource.com/platform/build/soong
    push-interval: 5s
```

### Root Configuration (`GitPusherPoolConfig`)

| Field | Type | Description |
|---|---|---|
| `name` | string | Name of the pusher pool. |
| `defaults` | [`GitPusherDefaultsConfig`](#gitpusherdefaultsconfig) | Default configuration for pushers. |
| `pushers-count` | int | Total number of pushers in the pool. |
| `pushers` | list of [`GitPusherConfig`](#gitpusherconfig) | Configuration for individual pushers. |
| `repos` | list of [`RepoConfig`](#repoconfig) | Configuration for repositories to watch (manifests). |
| `projects` | list of [`ProjectConfig`](#projectconfig) | Configuration for individual projects. |

### `GitPusherDefaultsConfig`

Defines default configuration for pushers. These can be overriden per pusher.

| Field | Type | Description |
|---|---|---|
| `port` | int | Listening port of the Git pusher. |
| `max-remote-pushes` | int | 100 | Maximum concurrent pushes to the remote. |
| `max-projects-syncs` | int | 10 | Maximum concurrent project syncs. |
| `max-memory-usage` | float64 | 0 | Maximum memory usage percentage (0-100) to self throttle. |

### `GitPusherConfig`

Defines a specific pusher instance.

| Field | Type | Default | Description |
|---|---|---|---|
| `name` | string | - | Unique name of the pusher. |
| `index` | int | The position of the item. | Index of this pusher (0 to `pushers-count` - 1). |
| `inactive` | bool | `false` | If true, this pusher is disabled. |
| `url` | string | - | URL of the Git pusher. |
| `port` | int | - | Listening port of the Git pusher. |
| `max-remote-pushes` | int | 100 | Maximum concurrent pushes to the remote. |
| `max-projects-syncs` | int | 10 | Maximum concurrent project syncs. |
| `max-memory-usage` | float64 | 0 | Maximum memory usage percentage (0-100) to self throttle. |


### `RepoConfig`

Defines a repository to watch, typically a manifest repository that points to other projects.

| Field | Type | Description |
|---|---|---|
| `manifest` | [`ManifestConfig`](#manifestconfig) | Configuration for the manifest file. |
| `project-defaults` | [`FetchConfig`](#fetchconfig) | Default fetch settings for projects found in the manifest. |
| `overrides` | map of string -> [`OverrideFetchConfig`](#overridefetchconfig) | Config overrides for specific projects (non-zero values replace project-defaults) |

### `ManifestConfig`

| Field | Type | Default | Description |
|---|---|---|---|
| `url` | string | - | URL of the manifest repository. |
| `server` | string | - | Server address (if URL is not full). |
| `project` | string | - | Project path (if URL is not full). |
| `files` | list of [`ManifestFile`](#manifestfile) | - | List of manifest files to include. |
| `check-interval` | duration | 60s | Interval to check for manifest updates. |

### `ManifestFile`

| Field | Type | Description |
|---|---|---|
| `branch` | string | Branch to look for the manifest file. |
| `file` | string | Path to the manifest XML file. |

### `ProjectConfig`

Defines an individual project to sync.

| Field | Type | Description |
|---|---|---|
| `server` | string | Server address. |
| `project` | string | Project path. |
| `url` | string | Full URL (alternative to server/project). |
| *(inline)* | [`FetchConfig`](#fetchconfig) | Inherits all fields from FetchConfig. |

### `OverrideFetchConfig`

| Field | Type | Default | Description |
|---|---|---|---|
| `exclude` | bool | `false` | If true, this project will be excluded. |
| `regex` | bool | `false` | If true, the key is a regex to match project name. |
| *(inline)* | [`FetchConfig`](#fetchconfig) | Inherits all fields from FetchConfig. |

### `FetchConfig`

Shared configuration for fetching and syncing settings.

| Field | Type | Default | Description |
|---|---|---|---|
| `fetch-ref-spec` | list of string | `refs/*:refs/*` | Refspecs to fetch. |
| `negative-fetch-ref-spec` | list of string | - | Refspecs to exclude (must start with `^`). |
| `push-interval` | duration | 60s | Interval between pushes/syncs. |
| `pack-push-interval` | duration | 30m | Interval between pushing the git pack files. |
| `lfs` | bool | `false` | Enable LFS support. |
| `workers` | int | 20 | Number of workers for DAG sync. |
| `packing-strategy` | none, commits, trees, blobs-list, blobs | trees | The strategy for pushing the git pack files. |

## Notes

- **Durations**: strings like "60s", "1m", "1h".
- **URLs**: If `url` is missing, it is constructed from `defaultProtocol` (https) + `server` + `project`.
