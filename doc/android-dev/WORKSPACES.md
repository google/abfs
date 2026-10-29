# ABFS Workspaces

## The Concept: What is an ABFS Workspace?

An ABFS workspace is a personal view of the
Android source code at your chosen revision. Changes you make to files, git
state, and repo state, are all saved in the workspace and persisted between
sessions. A workspace could be mounted multiple times on the same machine or
across different machines and the files will stay in sync.

## Current limitations

### NOT FOR PERMANENT SOURCE CONTROL

> [!WARNING]
> Important changes should be backed up in a remote location like Gerrit.

Your ABFS System Admin can provide you with more details, but the current server-side release only supports a single disk and will fill up and need to be wiped periodically. So while ABFS will use best effort to sync your workspaces to the server, the server will not live indefinitely to persist them. Staging your changes on a Gerrit server or some other remote location is recommended if you aren't merging or discarding them.

### `out` directory missing

The `out` directory is not stored with the workspace due to ABFS performance
reasons. Rebuilding the workspace should be fast since everything will be stored
in the cache.

### Mount at the same path on every host

Build outputs can depend on the path where the workspace is mounted. ABFS caches a
build step by its command and inputs, so a step whose result differs by path gives
hosts at different paths different cache entries, and a replayed output can point
into another host's workspace.

The Android build writes absolute symlinks into `out/` in at least two places:

- Soong `prebuilt_build_tool` modules (for example `bison`, `flex` and `m4`) link to
  `$PWD/<source>` on Android 15 and 16. Android 17 copies these tools instead.
- Make installs a host prebuilt (executable, shared library or native test) whose
  source is a symlink as a link to the `realpath` of that source, on Android 15, 16
  and 17.

The link target is computed when the step runs, so the step has the same cache key
on every host but a different result per path. When hosts at different paths each
run such a step, the cache holds one result per path, and a later build applies one
of them. The steps that read the link miss the cache when the applied result is not
the one they were cached with, and run through a link into another host's path.
They fail where that path does not exist, and would read another workspace's files
where it does. When every step hits, the replayed links point into the workspace of
the host whose result was applied.

Code outside AOSP can do the same, for example compiler flags without
`-ffile-prefix-map`, kernel build options that embed paths, builds that another
build system runs inside one ninja step, and commands that contain
`$(abspath ...)` or `$(PWD)`.

Mount the workspace at the same absolute path, for example `/workspace/android`, on
every host that shares a cache.

#### Check a tree for path-dependent outputs

Run these in the workspace root after a build. `find` uses the path as a pattern, so
it must not contain `*`, `?` or `[`.

```sh
# Absolute symlinks in out/ that point into the workspace.
find out -type l -lname "$PWD/*" -printf '%p -> %l\n'
# Ninja lines that contain the workspace path, per file. A step whose command
# contains the path has a different cache key at every path. Some releases name the
# Soong shards build.<product>.ninja.0 and so on, hence the second glob.
grep -cF "$PWD/" out/*.ninja out/soong/*.ninja*
```

Links made with `$PWD` record the path the build shell had, and links made with
`realpath` record the physical path. If `pwd -P` prints a different path than `pwd`,
for example because a parent directory is a symlink, run the `find` again with
`$(pwd -P)` in place of `$PWD`.

To find the steps that miss the cache at another path, build the tree in one
workspace, then build it once in a new workspace at another path and run there:

```sh
awk '!/^#/ && $3 != "cached" && $3 != "delayed-cached" {print $3, $7}' out/.ninja_log
```

`out/.ninja_log` keeps the steps of every build in the workspace, so use one that
has been built once. A few steps, such as the ones that write the build number and
the host name, run locally in every build. This build stores its results for the
steps it runs, like any other build.

To find files in `out/` that contain the workspace path, for example in debug info
or generated sources, run in the workspace root:

```sh
grep -rlF --binary-files=text "$PWD/" out \
  --exclude='*.ninja' --exclude='*.ninja.*' --exclude='.ninja_*' \
  --exclude='*.log' --exclude='*.gz' --exclude='*.pb' --exclude='soong_metrics'
```

This reads every file in `out/`, which fetches every output that is not local yet.
Other build logs and metrics files can still be listed; they are not build outputs.

To list the steps whose results differ between two builds at different paths, build
the tree in workspace A as usual. In workspace B at another path, add
`--salt mount-path-check --no-cache` to the `abfs setup` command you normally use for
the tree, so B runs every step itself and stores its results under a salt no other
build uses. Build once in B, then compare the two builds:

```sh
abfs diff-build -q $MOUNT_A/out/.ninja_log $MOUNT_B/out/.ninja_log
```

`diff-build` reads the two logs as files and the build results from the cache, so on
two hosts copy B's `out/.ninja_log` to A's host first.

Each line is a step whose reads or writes differ. It starts with the run types of both
builds printed together, followed by `*` when no other listed step explains the
difference (for example `locallocal*`). Then come the request hashes with `==` or `!=`
for whether the step's inputs differ, the response hashes with `!=`, and the target.
The steps marked `*` are where the path enters the build; the others read their
outputs. `Changes` under `Summation` lists the files that differ in the steps marked
`*`, and `-v` prints the differing entries on stderr. Steps that differ for other
reasons, such as the ones that write the build number and the host name, are listed
too. Both logs must come from one build each; otherwise `diff-build` prints
`Logs contain different number of sections` and shows nothing. Run your normal
`abfs setup` command in B afterwards to go back to its settings.

## Managing mounts

Mounts and Workspaces are separate concepts in ABFS due to the nature of Linux
filesystems and how Android was developed historically. This can be confusing,
but is an important distinction.

Workspaces can have 0 or more mounted locations. In advanced use cases, mounts
could exist without a workspace, but this isn't suggested for users. This
section contains use cases where mounts and workspaces are linked.

### Mount with default workspace name

By default, a workspace is named based on the directory where it is mounted. So
the simplest way to use workspaces is to not even specify a workspace name.

```sh
abfs mount -b main ~/feature-a
```

In this scenario, your workspace name will be `feature-a`.

### Mount with a chosen workspace name

This allows you to specify a name for the new workspace.

```sh
abfs mount -b main --ws feature-a ~/my/android/dir
```

### Unmount a workspace

This will unmount the workspace from the specified location to free up local
resources. The workspace will be preserved in the ABFS store so it can be
remounted later.

```sh
abfs unmount ~/feature-a
```

### Unmount and delete a workspace

This will unmount the workspace and delete it from the ABFS store.

> [!WARNING]
> You will lose your unsubmitted changes!

```sh
abfs unmount --rm ~/feature-a
```

### Unmount all workspaces

This will just unmount, **not** delete the workspaces.

```sh
abfs unmount --all
```

### Unmount and kill processes using a workspace

By default, ABFS will fail if you have a process using the workspace - even a
bash terminal.

You can kill these processes with this command:

```sh
abfs unmount ~/feature-a --kill
```

Or you can unmount all your workspaces and kill processes:

```sh
abfs unmount --all --kill
```

### Remount a workspace

This can be used to restore previous state if you `unmount`ed a workspace. Or
you can create multiple mounts for a single workspace on the same machine or
across multiple machines.

```sh
abfs remount ~/feature-a
```

You can also provide a workspace name just like `mount`.

```sh
abfs remount --ws feature-a ~/my/android/dir
```

### List mounts

Mounts should be cleaned up when not in active use to free up system resources.
With this command you can see what workspaces you have mounted:

```sh
abfs mount --list
```

### Show the workspace linked to the current directory

If you are inside of a workspace, you can do this:

```sh
abfs ws
```

Or more verbose:

```sh
abfs ws --long
# alias:
abfs ws -l
```

## Managing workspaces (without mounts)

Since workspaces live remotely in the ABFS store, these actions can be done
without having a workspace mounted. If you are looking for commands to manage
workspaces that are mounted, see [Managing mounts](#managing-mounts).

### List workspaces

Lists all workspaces registered in the ABFS store.

```sh
abfs ws list
```

Or with more detail:

```sh
abfs ws list --long
# alias:
abfs ws list -l
```

### Clone/Duplicate a workspace

Duplicates an existing workspace to a new name.

```sh
abfs ws clone feature-a feature-b
```

After cloning, you will need to use `remount` to mount the clone somewhere:

```sh
abfs remount ~/feature-b
# or:
abfs remount --ws feature-b ~/my/android/dir
```

If you just need to mount an existing workspace to a new location but want both
mounts to stay in sync, use [remount](#remount-a-workspace) directly and skip the clone
step.

### Remove a workspace

Deletes the remote workspace from the ABFS store.

> [!WARNING]
> You will lose your unsubmitted changes!

```sh
abfs ws remove feature-a
```
