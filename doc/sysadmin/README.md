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

### CAS server

Start the main CAS gRPC server with an existing backing directory.

> [!WARNING]
> By default this server has no TLS/authentication/authorization enabled

```sh
SERVER_DIR=
mkdir -p "$SERVER_DIR"
# Can use any available port, 50051 is just an example. This will produce a lot of logs
abfs server --port 50051 file-based --local-dir "$SERVER_DIR"
```

### Configuration

Some configuration of ABFS is still done through command-line flags, but we are trying to move everything to use configuration stored in the CAS itself for dynamic update purposes. We call this feature Hashpaths and you can read more in the [Hashpath code comments](../../shared/hashpath/README.md).

#### Set up a client

The bootstrapping process could use some improvements here, but you currently need an abfs client configured to write these hashpaths. So start by running `init` on your client machine to set up the background cache manager (cacheman) systemd service:

```sh
SERVER=
abfs --remote-servers $SERVER:50051 --disable-tls init
```

Ensure the below exits successfully, which shows that it has a connection to the CAS server. It prints `retrying...` while cacheman is still starting.

```sh
abfs cacheman remote-ok
# No output is good output, but you can also check the exit code
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
EOF
```

### Git-pusher servers

This is run on each of the git-pusher servers.

#### Authentication with git

For authentication, git-pusher will use the git binary at `/usr/bin/git` (this can be changed with the `--git-bin` flag). This binary needs to be able to read your git repos via your global git configuration. So ensure you have set it up and run a test `git clone` to see if that works.

For example, to authenticate against a Gerrit instance as a Google user, you would visit the `/new-password` page e.g. https://android.googlesource.com/new-password. It then gives you a command to write a `~/.gitcookies` file and configures `git` to use it.

#### Run git-pusher

On each of your git-pusher servers, run the following:

```sh
PUSHER_DIR=
SERVER=
mkdir -p "$PUSHER_DIR"
# pass --instance <name> if you don't want to use the default of `hostname` output
# this will produce a lot of logs
abfs --remote-servers $SERVER:50051 --disable-tls git-pusher --storage-path $PUSHER_DIR
```

### Default Client Configuration

This is run on the client you set up above.

Every client running abfs has their own configuration file. This is written by their initial `abfs init` and can be viewed and edited with `abfs config` flags and subcommands. However, most clients connected to your CAS server should be using the same configuration, so we have this feature to manage most of the configuration centrally.

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
