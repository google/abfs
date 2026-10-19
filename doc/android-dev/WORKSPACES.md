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
