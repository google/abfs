# ABFS: Another Build File Service

ABFS is a content-addressable file system and build cache that speeds up
Android source checkouts and builds.

Every file and directory is identified by the SHA-1 hash of its content,
following the Git object model. Because of this, ABFS can mount a source tree
for any manifest almost instantly and load files lazily the first time they
are read. Identical content is stored only once, and build results can be
cached and shared by content.

> [!NOTE]
> This is not an officially supported Google product.

## How it works

*   **Client.** The `abfs` CLI mounts source trees and runs builds. The
    `cacheman` daemon manages the local cache and is the only component that
    talks to the remote server. Mounts use either the CASFS kernel module or
    the FUSE implementation in [`casfuse/`](casfuse). Both get content from
    `cacheman` on demand.
*   **Server.** The server stores objects by hash (the object store) and maps
    the hash of a build step to the hash of its result (the function map).
    This repository includes file-based and in-memory server backends.
*   **Pusher.** The pusher (`abfs git-pusher`) fetches Git objects
    from a Gerrit host and uploads them to the server.
*   **Build acceleration.** [ninja-go](https://github.com/google/ninja-go)
    replaces timestamp checks with content hashes. For each build step, the
    ABFS backend in [`ninjago/`](ninjago) hashes the command, its inputs and
    its environment, and looks the result up in the function map. On a hit,
    ABFS materializes the cached outputs. On a miss, it runs the command,
    records every file read and written (tapping), and stores the result.

See the [user guide](doc/UserGuide.md) for setup and usage.

## Related projects

*   [abfs-casfs](https://github.com/google/abfs-casfs): the CASFS Linux kernel
    module, the fastest way to mount ABFS trees.
*   [ninja-go](https://github.com/google/ninja-go): the Ninja reimplementation
    that ABFS plugs its build backend into.

## Building

You need Go (see `go.mod` for the version) and a Linux host.

To build and install all binaries into `$GOBIN` (or `$GOPATH/bin`), run
`make` from the repository root:

```sh
make
```

Set `DESTDIR` to install somewhere else:

```sh
DESTDIR=/install/here make
```

`make` also installs a Git pre-commit hook. It grants file capabilities to
`casfs-mounter`, `casfuse-mounter` and `abfs-resources` with `setcap`, which
may prompt for a `sudo` password. Set `SKIP_SETCAP=1` to skip this step, for
example when packaging. The install directory must support extended
attributes (NFS does not).

Other useful targets:

| Target | Builds |
| :--- | :--- |
| `make abfs` | Only the `abfs` CLI. |
| `make install-client` | Everything a client needs. |
| `make install-server` / `make install-pusher` | Only the `abfs` binary, for servers and pushers. |
| `make ninjago` | `ninjago` with the ABFS backend. |
| `make clean` | Removes the installed binaries. |

To just compile everything:

```sh
go build ./...
```

## Testing

```sh
go test ./...
```

Some tests need a mounted CASFS file system and are skipped otherwise. To run
them, use `cas/tests/api_test.sh` (kernel module) or
`use_fuse_mount=1 cas/tests/api_test.sh` (FUSE).

## Repository layout

| Directory | Contents |
| :--- | :--- |
| `cmd/` | Binaries: `abfs`, `abfsbox`, `abfs-wrap`, `ninjago`, the mounters, and others. |
| `shared/` | Shared libraries: `cacheman`, local cache, remote client and servers, entities. |
| `cas/` | Content-addressable storage library and the `cas`/`casfs` CLIs. |
| `casfuse/` | FUSE implementation of CASFS. |
| `ninjago/` | The ABFS backend for ninja-go. |
| `bazeldaemon/` | Bazel remote execution and output service integration. |
| `gitpusher/` | Gerrit-to-ABFS pusher. |
| `manifest/` | Parser for `repo` manifests. |
| `rendezvous/` | `Chasher`, which assigns work items to one of N workers (built on `third_party/rendezvous`), and the `chash` CLI. |
| `third_party/` | Vendored third-party code: `bazel` (Remote Output Service protos), `cwalk`, `go-git` (Git hash and file mode helpers), and `rendezvous` (rendezvous hashing, used to shard work across pushers). |
| `doc/` | User documentation. |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). This project follows
[Google's Open Source Community Guidelines](https://opensource.google/conduct/).

## Security

Eligibility for the [Google Open Source Software Vulnerability Rewards
Program](https://bughunters.google.com/open-source-security) is determined by
the [Google Open Source Software Vulnerability Reward Program
Rules](https://bughunters.google.com/about/rules/open-source/google-open-source-software-vulnerability-reward-program-rules).

## License

Apache 2.0; see [LICENSE](LICENSE).

Some code comes from other projects and keeps its original license:

*   `third_party/bazel`: Apache 2.0 (The Bazel Authors), see
    [third_party/bazel/README.abfs.md](third_party/bazel/README.abfs.md).
*   `third_party/cwalk`: MIT, see
    [third_party/cwalk/README.abfs.md](third_party/cwalk/README.abfs.md).
*   `third_party/go-git`: Apache 2.0 (Sourced Technologies, S.L.), see
    [third_party/go-git/METADATA](third_party/go-git/METADATA).
*   `third_party/rendezvous`: MIT, see
    [third_party/rendezvous/README.abfs.md](third_party/rendezvous/README.abfs.md).
