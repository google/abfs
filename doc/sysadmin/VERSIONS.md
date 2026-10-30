# ABFS Versions and Upgrades

ABFS is several programs that run on different machines: the CAS server, the git-pushers, and on
each client machine the `abfs` binaries (CLI, cacheman, mount daemon, ninjago) and the CASFS
kernel module. This page describes which of them have to match, how to check what you run, and
how to upgrade.

## What has to match

* **On a client machine**, install the `abfs` binaries and the CASFS kernel module from the same
  release. They check each other's interface version before every mount (see
  [Check what you run](#check-what-you-run)).
* **The git-pushers of one pool** run the same release. Each pusher works out on its own which
  projects it pushes, from the pool configuration. Pushers of different releases are not checked
  against each other.
* **The CAS server and the clients** check each other's protocol version on every call. This is
  a separate number from the release, and a server and clients built from the same release
  always pass. A server refuses an older client with `PermissionDenied` and "client version
  invalid"; a client refuses an older server with "server version ... is less than minimum
  supported version".

## Check what you run

On a client machine:

```sh
abfs version --all
```

```
client: 0.1.23-oss
cacheman: 0.1.23-oss
server: 0.1.1
kernel: 0.1.23-oss
```

* `client` is the release of the `abfs` binary you ran.
* `cacheman` is the release of the cacheman that is running. After you install a new `abfs`, it
  shows the old release until you run `abfs cacheman restart`, and some `abfs` commands log
  `Detected running cacheman using different executable or older inode`.
* `server` is the protocol version the server speaks, not its release.
* `kernel` is the release of the loaded CASFS module. A module built from a source archive
  without git history reports the number in its `VERSION` file, for example `0.1.23`.

To check that the parts work together:

```sh
abfs doctor -v
```

`abfs mount` and `abfs remount` run the same checks before they mount, unless you pass
`--skip-health-check`; `abfs ws mount` always runs them. The version checks report:

| Message | What to do |
| :---- | :---- |
| `CASFS module is not inserted` | Load the module (`sudo modprobe casfs` or `sudo insmod casfs.ko`). |
| `CASFS module does not expose implemented interface version` (or `required`) | The module is older than the client. Install the module from the client's release. |
| `CASFS implemented interface version is older than ABFS required version` | Install the module from the client's release. |
| `ABFS client implemented interface version is older than CASFS required version` | Install the client from the module's release. |
| `Cacheman implemented interface version is older than ABFS required version` | Run `abfs cacheman restart`, so cacheman runs the binary you installed. |
| `ABFS client implemented interface version is older than Cacheman required version` | Install the client from cacheman's release. |

## What a new release can change

Two changes between releases do not cause errors, but you will notice them:

* **The local cache format.** If it changed, cacheman empties the client's cache when it starts
  and logs `Uncorrectable cache incompatability detected. Cleaning cache...`. Objects are
  downloaded again as they are used.
* **The build cache key format.** If ninjago's request format changed, builds miss the cache
  until builds of the new release have filled it again.

## Upgrade a client machine

1. Unmount all workspaces. Nothing is deleted:

    ```sh
    abfs unmount --all
    ```

2. Install the new `abfs` binaries as described in [Install](../../README.md#install).
3. Replace the CASFS kernel module. The old module can only be removed when nothing is mounted:

    ```sh
    sudo rmmod casfs
    ```

    Then build and insert the module from the same release as in
    [Install CASFS](../../README.md#install-casfs).

4. Restart cacheman so that it runs the new binary:

    ```sh
    abfs cacheman restart
    ```

5. Check the versions and the health checks:

    ```sh
    abfs version --all
    abfs doctor -v
    ```

6. Mount your workspaces again, see [Remount a workspace](../android-dev/WORKSPACES.md#remount-a-workspace).

## Upgrade the server and the git-pushers

* **CAS server.** Stop the old server and start the new one. Clients that use the server while
  it restarts can see failed calls, so restart it between builds.
* **Git-pushers.** Stop every pusher of the pool, then start them all on the new release.

## Build with `make`

`make` stamps the release from `git describe` into every binary. A plain `go build` reports
`0.0.0`, and then:

* `abfs version` shows `0.0.0`,
* the patched `repo` that the CAS server stores treats the client as an old release and passes
  fewer arguments to `abfs repo sync`.

To set the version yourself, pass it to `make`, for example `VERSION=0.1.23-mybuild make`.
