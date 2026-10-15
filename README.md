# ABFS: Another Build File Service

Another Build File Service (ABFS) speeds up the checkouts and builds of large code repositories. The initial focus is on accelerating Android development but there is also some ongoing work to add Bazel support. Internally at Google, we see Android checkouts in seconds and builds in minutes.

> [!NOTE]
> This is not an officially supported Google product.

## Why ABFS?

ABFS enables lightweight code checkouts and enables builds to share a cache of previously performed build actions. If you have CI running builds, multiple developers running builds, or develop across multiple Android branches, ABFS can help you.

### What speedup can I expect with ABFS?

Below is the mean time of 3 Android checkouts and builds on a GCE n2d-standard-64 VM with 64 vCPUs and 256GB of RAM. The ABFS remote server was running in the same VPC as the client.

#### Checkout

|  | Mean Time | Speedup |
| :---- | :---- | :---- |
| Traditional | 36m19s | 1.0x |
| ABFS mount with empty local cache | 7.4s | **294.5x** |
| ABFS mount with warm local cache | 3.6s | **605.3x** |

The “Traditional” flow used `repo init --partial-clone` and `repo sync -j8` as documented in [the Android docs here](https://source.android.com/docs/setup/start#initialize_and_sync_the_code).

#### Build

|  | Mean Time | Speedup |
| :---- | :---- | :---- |
| Traditional | 1h20m35s | 1.0x |
| ABFS build with cache use disabled | 1h39m11s | *0.81x* |
| ABFS build with empty local cache but full remote cache | 2m11s | **36.9x** |
| ABFS build with full local cache | 1m19s | **61.2x** |

All of these builds used `lunch aosp_cf_x86_64_only_phone-aosp_current-userdebug` and `m droid` to build.

The “ABFS build with cache use disabled” is expected to be slower than a traditional build. ABFS’s speedups come from caching and reusing previously built actions. If a build step is being executed for the first time, ABFS still needs to execute the step. ABFS’s Content-Addressable Storage Filesystem (CASFS) has some overhead to provide the content-addressable features. It also has the overhead of “tapping”. Tapping records the actual reads/writes/deletes of each action. The tapping feature enables caching of the build steps.

The “ABFS build with empty local cache but full remote cache” is the common experience developers can expect before editing files. They might frequently sync to HEAD or check out a new branch. When they build for the first time, they will pull from the remote ABFS build cache that has been populated by a Continuous Integration setup or by other developers.

### How does it work?

At its core, ABFS has a Content-Addressable Storage Filesystem (called CASFS, implemented as a kernel module at [github.com/google/abfs-casfs](https://github.com/google/abfs-casfs)). This custom filesystem allows the directory structure and file info to be available while the file content is lazy-loaded and streamed on demand. This enables checkouts in seconds because clients only need to download project and directory info.

The second main part is a content-addressable build. Traditional Android builds use Soong to write ninja files, then use Ninja to execute those ninja files. Ninja is similar to Make in that it uses file modified timestamps to determine when to rebuild an output. Ninja-go ([github.com/google/ninja-go](https://github.com/google/ninja-go)) is our ninja compatible implementation that supports multiple backends. This repository contains a backend that is content-addressable. Ninja-go can use file content hashes to determine when to rebuild an output. It also works with the shared remote cache to use read and write build steps.

### Who will struggle with ABFS?

**Poor Networking -** ABFS relies heavily on the remote cache. You can imagine it as a more demanding networked file system, so ABFS requires clients to be online and have strong network performance.

**Staffing Requirements -** The remote cache has multiple services (see [System Admin docs](doc/sysadmin/README.md)) that need to be maintained by a system administrator. Additionally, monitoring is required to ensure build determinism and minimize developers introducing non-hermetic build steps.

**Resource Costs -** Although full ABFS adoption is expected to lower overall machine expenses by leveraging caching and enabling smaller client footprints, setting it up initially demands additional compute power and substantial storage for the cache.

**Non-Deterministic Builds -** If you maintain a fork of Android that has non-deterministic Android build steps, ABFS will not help for these steps. ABFS prioritizes correctness over cache hit rate. Dates, times, build numbers, etc. can pollute file names and file content and reduce cache hit rate. Also, the environment variables visible to each build step need to match for a cache hit, so disabling Soong sandboxing or otherwise changing environment variables will reduce cache re-use. However, ABFS does have features and tools to find and diagnose this non-determinism so it can help you to fix problematic build steps. See [ABFS Expert/Build Hygienist docs](doc/abfs-expert/README.md).

## Install ABFS

### Prerequisites

Your machine likely has these, but you need to install them if not:

* `git`
* `getcap`/`setcap` from apt package `libcap2-bin`

Install these required packages:

```sh
sudo apt-get install build-essential golang-go
```

Check [go.mod](go.mod) for the current required golang version. Go 1.21 added the feature to download the required golang version if missing so 1.21 is the minimum.

### Install

1. Clone this abfs repo
2. `cd abfs`
3. Install:

    ```sh
    make
    ```

> [!NOTE]
> This will request `sudo` privilege to run `setcap` on the binaries `casfs-mounter`, `casfuse-mounter`, and `abfs-resources`. If you prefer to do this yourself, set `SKIP_SETCAP=1` and read the [Makefile](Makefile) to run `setcap` yourself.

### Add the install directory to your PATH

`make` installs the binaries into Go's bin directory (`$(go env GOPATH)/bin`, usually `~/go/bin`, or `$GOBIN` if you set it). Make sure it is on your `PATH`:

```sh
export PATH="$PATH:$(go env GOPATH)/bin"
```

Add that line to your shell startup file (e.g. `~/.bashrc` or `~/.zshrc`) to make it permanent, then open a new shell and check:

```sh
which abfs
```

With just `abfs` installed, you can set up the background systemd service (`cacheman`), inspect and interact with remote objects, run the CAS server, run the git-pusher servers, etc. So if you are just setting up a CAS server for example, you can skip the “Install CASFS” section below. If you want to `mount` and build Android, however, you will need to continue to install CASFS below.

## Install CASFS

See [github.com/google/abfs-casfs](https://github.com/google/abfs-casfs) for more details, but we unfortunately have only built the CASFS kernel module against a limited number of kernel versions and tested it on fewer. If you run into failures building here, you will need to fix the issues.

1. Clone the abfs-casfs repo.
2. `cd abfs-casfs`
3. Install:

    ```sh
    make
    ```

4. Insert the kernel module:

    ```sh
    sudo insmod casfs.ko
    ```

This won’t persist across reboots.

## Hello World

This section targets people who are just playing around with ABFS. If the full infrastructure has already been set up in your environment or at your company, skip to the [Android Developer docs](doc/android-dev/README.md). If you want to set up the full infrastructure, see [System Admin docs](doc/sysadmin/README.md).

With this setup, you can mount an empty content-addressable filesystem and play with the capabilities.

1. Initialize cacheman (the background local cache manager):

    ```sh
    abfs --local-only init
    ```

2. Run a CASFS mount in the background:

    ```sh
    MOUNT=mnt
    mkdir $MOUNT
    abfs casfs-mount --daemon $MOUNT
    ```

3. Create a directory and file inside the mount:

    ```sh
    cd $MOUNT
    mkdir dir1
    echo world > dir1/hello1
    ```

4. Now use the content-addressable storage (CAS) features of ABFS to see the hash of this file:

    ```sh
    abfs hash dir1/hello1
    ```

5. Now create a new empty file and use the hash from above to instantly duplicate the file. Then check the contents:

    ```sh
    HASH=$(abfs hash dir1/hello1)
    touch dir1/hello2
    abfs hash --set $HASH dir1/hello2
    cat dir1/hello2
    ```

6. This even works for directories (the target directory must be empty):

    ```sh
    DIR_HASH=$(abfs hash dir1)
    mkdir dir2
    abfs hash --set $DIR_HASH dir2
    ls dir2
    ```

7. Unmount so that we can see abfs reading from the local cache instead of the filesystem:

    ```sh
    cd ..
    abfs unmount $MOUNT
    rmdir $MOUNT
    ```

8. Do you have a hash lying around and forget what it was? You can `inspect` and `cat` it:

    ```sh
    abfs inspect $HASH
    abfs cat $HASH
    ```

9. Or you can `inspect` and `ls` a directory hash:

    ```sh
    abfs inspect $DIR_HASH
    abfs ls $DIR_HASH
    ```

10. ABFS understands hashes and paths together:

    ```sh
    abfs cat $DIR_HASH:hello1
    ```

11. When you are done, clean up this cacheman instance (and say "yes" to the confirmation prompt):

    ```sh
    abfs instance rm default
    ```

## Uninstall

If you have any mounts, you must unmount them all:

```sh
abfs unmount --all
```

Uninstall abfs (this will also try to remove CASFS):

```sh
abfs purge
```

## Documentation

* [System Admin](doc/sysadmin/README.md)
* [ABFS Expert / Build Hygienist](doc/abfs-expert/README.md)
* [Android Developer](doc/android-dev/README.md)
  * [Workspaces](doc/android-dev/WORKSPACES.md)

## Related projects

*   [abfs-casfs](https://github.com/google/abfs-casfs): the CASFS Linux kernel
    module
*   [ninja-go](https://github.com/google/ninja-go): the Ninja reimplementation
    that ABFS extends with its build backend

## FAQ

### What does this command or config option do?

ABFS has been under development internally for years and the documentation has not kept pace with the implementation. For this open source repository, we are starting by documenting the "happy paths" that have been tested externally and are known to work. You are free to explore the other commands and flags of the CLI tools with `--help`, but we don't guarantee that they are working or well tested outside of this documented subset. We hope to iterate quickly and unlock all of the ABFS features to external users.

### Is there a FUSE implementation of the filesystem as an alternative to the kernel module?

We have a FUSE implementation under development so you may see some references to it in the codebase. It is not feature complete or ready for use yet.

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
