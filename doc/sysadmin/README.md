# ABFS / System Admin

## High Level View

At its core, the ABFS remote service runs a Content-Addressable Storage (CAS) server. This server has all of the Android source code, build artifacts, and ABFS custom objects. We include the Android Source so that our ABFS objects can point to other valid objects in the CAS.

The source is pulled from your Gerrit (or other Git) host and pushed to the CAS server. Our component **Git-pusher** accomplishes this. It fetches new changes using git, pushes blobs, objects, refs, etc. to the CAS server, and sleeps before the next fetch. It supports horizontally scaling and is usually run with 3 pushers in the pool.

## First Time Setup

### Prerequisites

These instructions assume that you have installed ABFS on all of the machines you are working on. The client machine also needs CASFS installed. See the [main README doc](../../README.md) for installation instructions.

We suggest 5 machines total:

* 1 CAS server that needs a large disk. This will store a clone of your entire Android source history and also all build artifacts you produce. This initial release doesn’t contain any garbage collection features so this will need to be discarded when it is full. We run a 64 core VM with 512GB of RAM, but the CPU utilization is low and the minimum specs haven’t been tested here.  
* 3 git-pusher servers. We run 3 96 core VMs with 768GB of RAM, but again the CPU utilization is low. The initial run where they need to push your entire git history is more demanding than the maintenance running where only new changes are fetched and pushed. It’s possible to downsize the VMs after the initial push is complete, but the minimum has not been tested.  
* 1 client machine used for configuring and testing out the CAS server

<details>
<summary>CAS Server</summary>

The current release stores all source code, developer code changes, intermediate and build artifacts, etc. on a single server. This will need to be periodically wiped and started over since the contents are not garbage collected. The plan is to make this smoother in upcoming releases. There is a warning for developers in [WORKSPACES.md](../android-dev/WORKSPACES.md#not-for-permanent-source-control) that their changes should be merged or backed up to a different source control system since the ABFS server won't live forever.

Depending on your Android history, the full git-pusher source code run could take 2 TB or more. An initial full Android build can add around 150 GB with all intermediates, images, and zip files. If another ABFS build is done with the same exact source, it will take close to 0 GB incremental storage and just store some user metadata. As the source code changes, ABFS will deduplicate all of the intermediates that it can. But full builds on top of changed source code will still lead to changed images and zips. So you can still expect 25-40 GB of new data per full build that has source code changes.


</details>

### CAS server

Start the main CAS gRPC server with an existing backing directory.

> [!WARNING]
> This release is missing authentication and authorization. Anyone who has network access to the CAS server can read and write any data. Please ensure it is firewalled appropriately.

#### Choose a transport mode

You should choose a transport mode that satisfies your company's security requirements. You might choose to go without TLS if you are running everything inside the same isolated network. 

| | **With TLS** | **Without TLS** |
| --- | --- | --- |
| Traffic between clients, git-pushers, and CAS server | Encrypted and clients verify the server's identity | Plaintext |
| Setup | A server certificate (ideally from a CA your clients already trust) | No certificates |
| Client authentication/authorization | **None** (TLS doesn't identify clients) | **None** |

Based on your selection, you will need to provide different initialization instructions to your developers or anyone else using an ABFS client. So you should decide before onboarding clients because existing clients would all need to modify their configuration.

If you run a reverse proxy or other similar service that terminates TLS, you would still need to instruct clients to use the "With TLS" setup regardless of if you run TLS on the CAS server itself or not.

<details>
<summary>With TLS</summary>

**Get a server certificate**

ABFS clients will trust the operating system's CA store by default. If you use a server certificate that your client machines already trust, you won't need to distribute a certificate to all of the clients. Common sources:

* Your company's internal CA (if your clients already trust it)
* A public CA such as Let's Encrypt

If none of these are available, you can use a private CA and distribute a certificate to all of your clients.

Whichever option you use, the certificate must:

* Include the hostname that clients will use in their `--remote-servers` client configuration to connect to the CAS server. It's easiest to use a fully qualified name that all clients can use instead of a short name that only works within a specific network segment.
* Be a PEM file containing the server certificate followed by any intermediate certificates, plus a PEM private key.

**Start the CAS Server with TLS**

Fill in empty env vars and start the server:

```sh
TLS_CERT_FILE=
TLS_KEY_FILE=
SERVER_DIR=
mkdir -p "$SERVER_DIR"
# sample rate of 1 will log every request
LOG_SAMPLE_RATE=.01
# Can use any available port, 50051 is just an example
abfs server --port 50051 --log-sample-rate $LOG_SAMPLE_RATE --tls-cert-file "$TLS_CERT_FILE" --tls-key-file "$TLS_KEY_FILE" file-based --local-dir "$SERVER_DIR"
```

The output should include "TLS enabled." If you pass an empty `TLS_CERT_FILE` or `TLS_KEY_FILE`, it will fall back to running without TLS.

The server only reads certificates at startup so it needs to be restarted when the certificates are renewed.
</details>

<details>
<summary>Without TLS</summary>

> [!WARNING]
> This server has no TLS/authentication/authorization enabled

Fill in `SERVER_DIR` and start the server:

```sh
SERVER_DIR=
mkdir -p "$SERVER_DIR"
# sample rate of 1 will log every request
LOG_SAMPLE_RATE=.01
# Can use any available port, 50051 is just an example
abfs server --port 50051 --log-sample-rate $LOG_SAMPLE_RATE file-based --local-dir "$SERVER_DIR"
```

</details>

#### Health checks while serving

By default the server's gRPC health service reports `SERVING` for as long as the server runs, whatever the state of its storage. With `--health-probe-interval` the server probes its storage backend at that interval and reports `NOT_SERVING` for the overall status (service name `""`) and for the `abfs.ABFS` service while the backend fails, so that load balancers, Kubernetes readiness probes and other health checkers stop sending requests to it. Set it on every server behind a load balancer or watched by a health checker, together with `--drain-delay` (see below). It is a flag of `abfs server`, so it goes before the backend:

```sh
abfs server --port 50051 --health-probe-interval 10s --drain-delay 15s file-based --local-dir "$SERVER_DIR"
```

* A probe round runs the checks of the backend in parallel. It fails when a check returns an error or does not return within `--health-probe-timeout` (5s by default, not above the interval). A round still running when the next one is due counts as one more failure each interval, and its result is ignored when it comes. The server reports `NOT_SERVING` when `--health-probe-failures` (N, 3 by default) of the last 2N-1 rounds failed, and `SERVING` again after N rounds in a row passed. With the defaults and a 10s interval, the server reports `NOT_SERVING` 20 to 30 seconds after a backend failure starts when the checks fail at once, and up to 40 seconds after it when a check is slow or hangs; it reports `SERVING` again 20 to 30 seconds after the failure ends. A load balancer or readiness probe adds its own interval and thresholds on top.
* The file-based server checks that `--local-dir` is still the directory it started with, writes, syncs and removes a small file named `.abfs-health-probe` in it, and reads one stored object from disk. A full disk, a read-only remount, a permission change and a moved or unmounted directory fail the probe. The probe does not wait for the server's I/O limit, so a disk saturated by requests can fail it too.
* Other server types are probed only when they provide backend checks.
* The in-memory server has no backend to probe. With the flag set it logs that once and keeps reporting `SERVING`.
* Servers that share a backend fail their probes together, so a backend outage marks all of them `NOT_SERVING` at once. A load balancer then has no healthy server left: depending on the product it fails every request or fails open and sends requests to every server again, and a Kubernetes Service whose pods are all not ready has no ready endpoints, so connections to it fail. Running clients retry such calls under their retry policy; new client processes fail their connect check within 10 seconds with "error during health check". A failure of one backend takes the server out even when requests that do not use that backend would still work.
* With a single server and no load balancer, every new client process that does the connect check (the CLI, the `abfs mount` preflight, a git-pusher start) fails at connect with "not healthy, status was NOT_SERVING" while the backend is down. Cacheman and running mounts are not affected.
* The probe sets only the overall status and `abfs.ABFS`. A load balancer for proxied ARES traffic (`--ares-enabled`) should check the ARES service name, not the overall status.
* When a metrics provider is configured, two gauges follow the probe: `abfs_server_serving` is 1 while the server reports `SERVING` and 0 after the failure rule and from the shutdown signal on; `abfs_server_backend_up{backend}` is 1 for each backend whose check passed in the last round and 0 for one that failed or did not return in time. Every failed round logs its elapsed time and the error of each failed backend.
* On a shutdown signal the health service reports `NOT_SERVING` for good; the probe never sets it back to `SERVING`.

#### Stopping the server

The server stops gracefully on `SIGTERM` or `SIGINT` (Ctrl+C): it stops accepting connections, lets in-flight requests finish, closes its files and exits with status 0. Requests still running after `--shutdown-timeout` (25s by default) have their connections closed, and the server still exits with status 0, so a client that stopped reading or went away cannot keep the server from exiting. Size it from the time the largest transfer should be given to finish, and keep it below the time your service manager, container runtime or orchestrator waits before it kills the server. `--shutdown-timeout 0` waits for every request, however long it takes. It is a flag of `abfs server`, so it goes before the backend:

```sh
abfs server --port 50051 --shutdown-timeout 60s file-based --local-dir "$SERVER_DIR"
```

* The signal must reach the `abfs` process. If a wrapper script starts the server, start it with `exec` so that the server replaces the script.
* A repeated `SIGTERM` is ignored while the server shuts down. A `SIGINT` after the first signal makes it exit at once, without waiting for in-flight requests. Every command of the `abfs` binary (including cacheman and git-pusher) handles signals this way.
* With `--ares-enabled`, proxied ARES requests can run much longer than the default timeout; set `--shutdown-timeout` to match.
* On the first signal the server's gRPC health service reports `NOT_SERVING` for every service. Behind a load balancer or a health checker that uses it, set `--drain-delay` to keep serving new requests for that long before the server stops accepting connections, so the balancer moves traffic away first. Size it from the health check interval times the unhealthy threshold, and count it in the time your process manager waits: the delay comes before `--shutdown-timeout`. The default, `0`, starts the drain at once. A client that watches health with the streaming `Watch` call keeps its stream open until `--shutdown-timeout` closes it, so with `--shutdown-timeout 0` such a client keeps the server from exiting.

### Configuration

Some configuration of ABFS is still done through command-line flags, but we are trying to move everything to use configuration stored in the CAS itself for dynamic update purposes. We call this feature Hashpaths and you can read more in the [Hashpath code comments](../../shared/hashpath/README.md).

#### Set up a client

The bootstrapping process could use some improvements here, but you currently need an abfs client configured to write these hashpaths. So start by running `init` on your client machine to set up the background cache manager (cacheman) systemd service:

<details>
<summary>With TLS</summary>

If you used a private CA, also pass `--root-ca-file` with the absolute path to the certificate file before the `init` part of the command.

```sh
SERVER= # must match a name in the certificate
abfs --remote-servers $SERVER:50051 init
```

</details>
<details>
<summary>Without TLS</summary>

```sh
SERVER=
abfs --remote-servers $SERVER:50051 --disable-tls init
```

</details>

Ensure the below exits successfully, which shows that it has a connection to the CAS server. It prints `retrying...` while cacheman is still starting.

```sh
abfs cacheman remote-ok
# 0 exit code means connected
echo $?
```

If there are issues, you need to inspect `abfs cacheman tail` and debug.

#### Create required Hashpaths

This is run on the client you set up above.

Create the main config ref for storing hashpaths

```sh
abfs edit --add config:.
```

Replace the default values in the YAML below and then run this command to create the git-pusher config. Read about the many configuration options in the [git-pusher doc](../../gitpusher/README.md).

> [!WARNING]
> By default, git-pusher sleeps for 1 minute between runs of `git fetch` for every project in your manifest file. Ensure your Gerrit host can handle this increase in traffic.

* **port**: Git-pusher assumes it is running inside of a firewalled network, and will listen on the specified `port` below on all interfaces without TLS. Exclude the port and it won't listen on HTTP.
* **fetch-ref-spec**: By default, git-pusher will fetch and push all refs for every project in the provided manifest file. This is great if you want a complete history and the ability to mount based on any branch, tag, or change. But it isn't great if you only care about limited branches and minimizing disk usage by the CAS Server. See the comment in the YAML below if you want to include a **fetch-ref-spec** to limit the branches fetched and pushed.

```sh
abfs edit -a --stdin config:git-pusher/default << 'EOF'
name: default
pushers-count: 3 # replace with your count
defaults:
    port: 9998 # HTTP listening port for info and debug

pushers:
  - name: pusher-0 # each pusher's --instance name (defaults to `hostname` output)
  - name: pusher-1
  - name: pusher-2

repos:
  - manifest:
      url: https://android.googlesource.com/platform/manifest
      files:
        - branch: android-latest-release
          file: default.xml
    # uncomment the below 3 lines and use your manifest's default revision instead of "android17-release" if you only want limited branches pushed
    # project-defaults:
    #   fetch-ref-spec:
    #     - +refs/heads/android17-release:refs/heads/android17-release
EOF
```

### Git-pusher servers

This is run on each of the git-pusher servers.

#### Authentication with git

For authentication, git-pusher will use the git binary at `/usr/bin/git` (this can be changed with the `--git-bin` flag). This binary needs to be able to read your git repos via your global git configuration. So ensure you have set it up and run a test `git clone` to see if that works.

For example, to authenticate against a Gerrit instance as a Google user, you would visit the `/new-password` page e.g. https://android.googlesource.com/new-password. It then gives you a command to write a `~/.gitcookies` file and configures `git` to use it.

#### Run git-pusher

On each of your git-pusher servers, run the following:

<details>
<summary>With TLS</summary>

If you used a private CA, also pass `--root-ca-file` with the certificate path before the `git-pusher` part of the command.

```sh
PUSHER_DIR=
SERVER= # must match a name in the certificate
mkdir -p "$PUSHER_DIR"
# pass --instance <name> after `git-pusher` if you don't want to use the default of `hostname` output
# this will produce a lot of logs
abfs --remote-servers $SERVER:50051 git-pusher --storage-path $PUSHER_DIR
```

</details>
<details>
<summary>Without TLS</summary>

```sh
PUSHER_DIR=
SERVER=
mkdir -p "$PUSHER_DIR"
# pass --instance <name> after `git-pusher` if you don't want to use the default of `hostname` output
# this will produce a lot of logs
abfs --remote-servers $SERVER:50051 --disable-tls git-pusher --storage-path $PUSHER_DIR
```

</details>

### Default Client Configuration

This is run on the client you set up above.

Every client running abfs has their own configuration file. This is written by their initial `abfs init` and can be viewed and edited with `abfs config` flags and subcommands. However, most clients connected to your CAS server should be using the same configuration, so we have this feature to manage most of the configuration centrally.

Don't put `disable-tls` or `root-ca-file` into this central configuration. Clients must already set these flags with correct values locally to be able to connect to the server and download this config.

Replace the `manifest-server:` and any other needed values below for your setup. On your client machine, run the following:

```sh
abfs edit --add --stdin config:clients/configs << 'EOF'
# Applies to all clients
- select:
  config:
    manifest-scheme: https
    # replace with your gerrit host if not using AOSP
    manifest-server: android.googlesource.com
    manifest-project-name: platform/manifest
    git-prefetching: false
    concurrent-uploads: 200
    sync.auto-setup: true
    force-workspaces: true
    setup.psi-mem: 0
    # shouldn't be needed with 26Q4 release when build/soong/soong.environment.available is more deterministic
    ignore-soong-env-avail: true
EOF
```

This can always be changed later interactively with `abfs edit config:clients/configs`.

### Push repo

This is run on the client you set up above.

To enable faster source control operations, ABFS requires a slightly patched version of the [`repo` tool](https://gerrit.googlesource.com/git-repo). The patches enable `repo` to use the content-addressable feature of ABFS to speed up some operations. We store the patched version of `repo` inside the ABFS CAS server so that it can be cloned when developers mount a workspace. This will guide you through the initial seeding steps to patch `repo` and push it into the CAS.

1. Clone `repo` and apply the ABFS patches to it:

    ```sh
    abfs repo-setup
    ```

    It prints the directory of the patched clone, e.g. `Repository patched successfully in /tmp/repo-setup-1234567890`, followed by the commands for the next step.

2. Enter the `REPO_DIR` value below. Then push the patched clone to the CAS server and point `stable-abfs` at it:

    ```sh
    REPO_DIR= # the directory printed by abfs repo-setup
    abfs --no-local-cache push -r "$REPO_DIR"
    echo "$(git -C "$REPO_DIR" rev-parse HEAD) refs/heads/stable-abfs" | abfs test-client put-ref -s gerrit.googlesource.com -p git-repo
    ```

3. Check that the ref is there. This prints the commit hash followed by `refs/heads/stable-abfs`:

    ```sh
    echo refs/heads/stable-abfs | abfs test-client get-ref -s gerrit.googlesource.com -p git-repo
    ```

To update `repo` later, repeat these steps.

## First Mount

Unfortunately git-pusher does not yet have a great way to tell when it is complete and ready for clients to start mounting the source code. This is an area we need to improve. It could take a few days for the push to complete depending on the size of your git history and the number and size of your git-pusher servers.

The easiest way is usually just to try it out on your client machine and see which projects are still missing:

```sh
MOUNT=mnt
mkdir $MOUNT
abfs mount --branch android-latest-release --tmp-ws $MOUNT
```

Projects that haven't been pushed yet are reported as `project <name>: remote branch <revision> not found in upstream <server>/<name>`, followed by a `Failing projects:` list.

A failed mount stays mounted, so unmount it before you try again:

```sh
abfs unmount -k $MOUNT
```

If you want more details, each git-pusher exposes an HTTP `/info` endpoint that returns JSON on the port we set in the git-pusher config above. You can use `jq` (`sudo apt-get install jq` or see https://jqlang.org/download/) to summarize the project info with something like below run on **each** of the git-pushers:

```sh
curl -s localhost:9998/info | jq '{
  total:   (.AllProjects | length),
  done:    ([.Projects[] | select(.State=="sleep" and .LastError=="")] | length),
  failed:  [.Projects[] | select(.LastError!="") | {Name, LastError}],
  repos:   [.Repos[] | {Name, State, LastSuccess, LastError}]
}'
```

Then it's ready when `done == total`, `failed` is empty and every repo has a `LastSuccess`.

Once your mount succeeds, you are ready to move to the [Android Developer docs](../android-dev/README.md) and try out a build before sharing with your other developers.
