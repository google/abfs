# ABFS Guide

## Terminology

| Term     | Description                                                                                                                                                                               |
| -------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Client   | ABFS binary or process responsible for mounting and building Android                                                                                                                      |
| Pusher   | ABFS binary or process responsible for updating the ABFS server with Android source code                                                                                                  |
| Uploader | Synonym for Pusher                                                                                                                                                                        |
| Server   | ABFS binary or process responsible for serving clients                                                                                                                                    |
| Kmod     | Refers to the CASFS Kernel Module                                                                                                                                                         |
| Object   | When used in a general sense it can refer to any content addressable entity type. In strict terms, these are content addressable entities that are stored in the cache objects/ directory |

## Prerequisites

### Soong Version

The `platform/build/soong` project in the Android tree needs to be merged up to this change:\
https://android-review.googlesource.com/c/platform/build/soong/+/3619492

If you are using Android 14, merge up to this change instead:\
https://android-review.googlesource.com/c/platform/build/soong/+/3616708


## ABFS Config

ABFS reads a configuration file located at `~/.abfs/config`. When invoking `abfs init`, an initial configuration file is created, but should be updated to specify the correct server information. It is recommended to use the `abfs config` command to write to the configuration file. Command line overrides for configuration fields are supported for individual ABFS commands. Note that some fields are only used in specific scenarios.

The latest flags can be seen by invoking `abfs` without any arguments. A description of some common fields are below.

| Field                 | Description                                                         |
| --------------------- | ------------------------------------------------------------------- |
| remote-servers        | \<host:port\> tuple of the remote ABFS server                       |
| manifest-server       | Server to fetch the manifest from                                   |
| cache-dir             | Location on disk to store local cache data                          |
| fs                    | Recommend to leave as “kmod”                                        |
| enable-index          | Enable Index. Must be true to enable cache eviction                 |
| manifest-project-name | Name of the git project on the manifest-server containing manifests |


The following configuration field is used when a SSH tunnel is required to communicate with an ABFS server. For each
remote server (`<server-host>:<server-port>`) with a non-zero tunnel port, cacheman runs
`ssh -N -L <tunnel-port>:localhost:<server-port> -- <server-host>` and talks to the server through `localhost:<tunnel-port>`.

- Any SSH user, identity, or proxy settings for the server host should be configured in `~/.ssh/config`.
- ssh runs with `BatchMode=yes`, so authentication must work without prompting, for example with an `ssh-agent` that is
  available to the cacheman process.
- Unknown host keys are accepted on first use (`StrictHostKeyChecking=accept-new`); changed host keys are rejected.
- With TLS enabled, the client connects to `localhost`, so the server certificate needs `localhost` as a subject
  alternative name. Otherwise use `disable-tls`.
- If the tunnel fails, the reason reported by ssh is written to the cacheman log.

| Field          | Description                                                 |
| -------------- | ----------------------------------------------------------- |
| tunnel-ports   | Local port to use for each remote server's tunnel (0 = none) |


## ABFS Server

The ABFS server is responsible for long-term storage of ABFS objects, blobs, maps, and refs. It retrieves objects from the pushers and updates the branch mappings. Clients request objects from the server and send new ones.

```
LOG_FILE=/abfs/log.log
PORT=8080

abfs server --port $PORT file-based --local-dir /abfs/storage --levels 3 &> $LOG_FILE
```

The above command starts the ABFS server to listen on the provided port.

## ABFS Pusher

ABFS pushers are responsible for fetching git objects from Gerrit and pushing them to the ABFS server. It is possible to run with multiple pushers to distribute the load. Three pushers are commonly used.

```
WORKER_COUNT=3
LOG_FILE=/abfs/log.log

# On Pusher VM 0
WORKER_ID=0
abfs gerrit upload-daemon $WORKER_COUNT $WORKER_ID --branch main --project-storage-path /abfs/projects --interval 1m &> $LOG_FILE

...

# On Pusher VM 1
WORKER_ID=1
abfs gerrit upload-daemon $WORKER_COUNT $WORKER_ID --branch main --project-storage-path /abfs/projects --interval 1m &> $LOG_FILE

...

# On Pusher VM 2
WORKER_ID=2
abfs gerrit upload-daemon $WORKER_COUNT $WORKER_ID --branch main --project-storage-path /abfs/projects --interval 1m &> $LOG_FILE
```

The above commands will create three pushers, with each one responsible for a subset of the projects on the `main` branch. New git objects are queried every minute. During the initial seeding, the output log will have content similar to “platform/prebuilts/clang/host/linux-x86 still uploading”. The absence of these messages in the more-recent timestamped lines of the log is a signal indicating the individual pusher is up-to-date with Gerrit top-of-tree.

## ABFS Client

### Local Cache

The ABFS localcache is maintained on the client disk at ~/.abfs/cache. It is recommended to not modify this directory manually because the indexing and eviction state will become inconsistent. Below are some details on a few of the directories underneath ~/.abfs/cache.

#### Blobs

ABFS blobs are stored in ~/.abfs/cache/blobs. Blobs are simply files. Either source code or generated artifacts as part of a build. The relative file path under the blobs/ directory, with path separators removed, is the sha1 digest of the file contents. Files inside blobs/ are stored uncompressed and without any headers.

#### Objects

ABFS objects are stored in ~/.abfs/cache/objects. Objects can be anything that is not a blob. These include things like directory listings (aka trees), git commits, cache request objects, cache response objects, and more. The relative file path under the objects/ directory is the sha1 digest of the uncompressed file contents, excluding headers. Files inside objects/ are stored compressed and with headers compatible with the [git object model](https://git-scm.com/book/en/v2/Git-Internals-Git-Objects).

#### Map

Maps are stored in ~/.abfs/cache/map. Maps provide a way to relate one object to another, with one-to-many relationship support. An example use case for this is mapping a build request object to one or more build response objects. Mapping relations currently have an 8-hour time-to-live value at which point they are invalidated on the client.

### Cacheman

Cacheman is a long-lived process that manages the local cache and communicates with the remote server. It is responsible for pushing new objects and fetching missing ones.

#### Index and LRU
Cacheman maintains an Index describing the state of items known to the local cache. It also maintains a LRU structure used to evict objects when the local cache exceeds the size threshold specified in the ABFS configuration.

### Maintenance

Users can choose to clean the local cache on their own by executing the following command.
```
abfs cacheman clean -af
```

### Commands

All commands can be listed by running `abfs` without any arguments. This section details the more commonly used commands.

| Command  | Description                                                                                                |
| -------- | ---------------------------------------------------------------------------------------------------------- |
| cacheman | Interact with cacheman: query status, stop cacheman, start cacheman, restart cacheman, or inspect the log  |
| config   | Display or update the ABFS config. Note that some changes necessitate a restart of cacheman to take effect |
| init     | Create the local cache directory structure and start cacheman                                              |
| inspect  | Inspect the contents of a blob, object, or map                                                             |
| mount    | Create a casfs file system that serves an Android tree                                                     |
| setup    | Prepare an Android tree for building with ABFS                                                             |
| unmount  | Unmount the casfs file system                                                                              |

## Building

After the ABFS server has been populated with the Android source code by the pushers, builds can begin on client machines.

### Mount the Android Tree
An Android tree can be mounted with the `abfs mount` command. Example:

```
abfs mount -b main /src
```

The above command will mount the `main` branch, as known to the ABFS server, at the directory `/src`. This is analogous to a traditional `repo sync` to checkout a tree. The mount command will display a message of the form “Saved Mount-Details started at 1740000582854633610 with hash 28c24707a1b196dc0ff3c3aa14fde428e5ea87a4”, which provides the checksum of the `mount-details` object. This object can be viewed with `abfs inspect`.

### Setup the Android Tree

The Android tree needs to be prepared to build with ABFS. Notably this setup includes configuring the build to use `ninjago` rather than `ninja`.

```
cd /src # assuming mount was done at /src
abfs setup $PWD
```

### Build

After setup, the tree is ready to be built with ABFS. Example of building AOSP:

```
source build/envsetup.sh
lunch aosp_cf_x86_64_phone-trunk_staging-userdebug
m droid
```

After a build, a message of the form “Saved BuildInfo for 1740007373976431327 with hash eb2ae649e8b4be7a70100013a9f8b51229285207” will be displayed. This can also be viewed with `abfs inspect`.

Any artifacts generated during the build will be pushed to the ABFS server for use by other clients.

#### NSJail Configuration

`abfs nsjail` and `abfs setup` read the following environment variables. Both take paths relative to the top of the
source tree; absolute paths are rejected.

| Variable                 | Description                                                                                                |
| ------------------------ | ---------------------------------------------------------------------------------------------------------- |
| ABFS_NSJAIL_CHROOT_PATHS | Colon-separated list of candidate chroot directories. `abfs nsjail` uses the first one that exists. Required by `abfs nsjail`. |
| ABFS_NSJAIL_CONFIG       | NSJail build config XML that lists the overlays and views for each target. Optional; if unset, no overlays or views are applied. |

#### Known Issues

1. Nsjail is not supported with AppArmor [enabled](https://github.com/google/nsjail/issues/236).

```
# Disable AppArmor
sudo sysctl -w kernel.apparmor_restrict_unprivileged_unconfined=0
sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0
```

### Unmount

```
cd ~ # navigate anywhere outside of the mount
abfs unmount /src
```
