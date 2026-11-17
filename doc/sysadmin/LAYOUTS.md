# Tree layouts

This page is for the people who set up ABFS for CI/CD and for developers whose repo workspace
does not have the AOSP layout: what ABFS needs from such a tree, and how to check that a mount
is ready to build.

In AOSP the Android tree is the whole repo workspace: `build/soong`, `build/make` and
`prebuilts/build-tools` sit at the workspace root. Many product trees put Android in a
subdirectory instead, next to other parts of the product, for example:

```
.repo/
android/            # the Android tree: build/soong, build/make, frameworks/, prebuilts/, ...
other/              # another part of the product, built with its own tools
tools/              # scripts and host tools for the whole product
```

ABFS mounts, syncs and caches any such layout. The Android build cache needs ABFS's build
hooks in the Android tree itself, which `abfs setup` installs.

## Soong tops

A Soong top is a directory that Soong accepts as the root of an Android tree: it holds
`build/soong/root.bp` and `prebuilts/build-tools/linux-x86/bin/`. `build/envsetup.sh` and
`lunch` must run there, and that is where Soong looks for the ABFS hooks.

`abfs setup` finds the tops from the projects of the mount. It tests the mount root and every
directory that holds a `build/soong`, `build/make` or `build` project, and keeps those that pass
the test above.
A tree can have any number of tops at any depth (`android/`, `src/aosp/`, two Android
releases side by side, an SDK tree inside another tree). When no top is found, setup sets up
the mount root as before; if some project suggested a top, setup warns and names what each
candidate lacks (`build/soong/root.bp` or the prebuilt tools directory).

To name the tops instead, set the client config key `setup.tops` to a list of paths relative to
the mount root, for example in the central client configuration (see the
[sysadmin guide](README.md#default-client-configuration)):

```yaml
    setup.tops:
      - android
```

An entry that is not a directory on a mount is skipped with a warning, so one client config can
serve trees of different layouts; when no entry exists on a mount, setup finds the tops itself.

Or name them for one run:

```sh
abfs setup --top android $MOUNT
```

`--chroot` keeps its older meaning: a scratch checkout for `abfs nsjail`, left out of the
workspace snapshot. Use `--top` for an Android tree in a workspace.

The positional argument of `abfs setup` is the mount; any directory inside the mount means the
mount, so `abfs setup android` sets up the tops of the whole mount, not `android/`.

To see the tops and what is in place in each:

```sh
abfs setup --list $MOUNT
```

It prints each top with its `.ninjago.conf` (its `Chroot` is the top's path in the mount), the
generated files and binaries, the nsjail wrapper and, below the root, whether the top's
daemon socket answers. `--list` changes nothing.

## What setup installs in each top

- the ABFS binaries in `prebuilts/build-tools/linux-x86/bin/`: `abfsbox`, `ninjago`,
  `abfs-wrap`, `ckati-wrap`, `tap`, `rbetap`, `abfs-cmd-map`, `abfs-bazel-daemon`;
- `.ninjago.conf` with this client's cache directory and settings, and `Chroot` set to the
  top's path in the mount (empty at the root);
- `vendor/abfs/vendorsetup.sh`, which `source build/envsetup.sh` reads to select ninjago and
  to pin the build metadata (`BUILD_DATETIME`, `BUILD_HOSTNAME`, `BUILD_USERNAME`,
  `BUILD_NUMBER`) that would otherwise make some steps miss the cache on every build;
- below the root, `.repo/mount-details`, a relative link to the mount's `.abfs.mount-details`,
  and a daemon socket `.abfs.sock` for the build tools that run in the top;
- the nsjail wrapper where the top's Soong passes `{ABFS_DIR}` to nsjail (see the
  [Android developer guide](../android-dev/README.md#build)).

Setup copies the binaries from the `-no-cgo` files that `make install-client` installs (for
example `ninjago-no-cgo`). They must be on the `PATH` of the process that runs `abfs mount` or
`abfs remount`, which also runs setup; a CI runner or a systemd unit often has a shorter `PATH`
than a login shell. If one is missing, setup fails and names the file.

## When setup runs

With `sync.auto-setup: true` in the client config, as the sysadmin guide sets it, setup runs:

- after a classic mount (`abfs mount -b <branch>` without a workspace) has created the projects;
- after every `abfs repo sync`, including the one a workspace mount runs;
- after `abfs remount` restores a workspace.

`sync.auto-setup` controls all of them; the separate `auto-setup` key has no effect. If setup
fails, the command fails with setup's error: a classic mount and `abfs repo sync` report it, and
`abfs remount` unmounts the workspace again. Fix the cause, often the `-no-cgo` files missing on
`PATH`, and run the command again.

With `sync.auto-setup` off, run `abfs setup $MOUNT` yourself after every mount and remount. A
workspace snapshot does not keep `.ninjago.conf`, so after a remount ninjago has no
configuration until setup runs.

Setup rewrites its files on every run. If a build is running in a top (`out/.build_start`
without `out/.build_info`), setup leaves that top's binaries alone and says so; run it again
after the build.

## What a workspace keeps

A workspace snapshot keeps every file under a top, including files you create outside any
project, except:

- `out/` and `dist/`, as in every workspace;
- `.ninjago.conf` and `vendor/abfs/vendorsetup.sh`, which hold this client's settings and are
  written again by setup on the next mount or remount. If the tree has no `vendor/` project,
  setup creates `vendor/` itself and the snapshot leaves out all of it, so keep your own files
  under `vendor/` inside a project.

## Building

Run the Android build from the top:

```sh
cd $MOUNT/android
source build/envsetup.sh
lunch <target>
m
```

`build/envsetup.sh` finds the top from the current directory and reads `vendorsetup.sh` there.
That file selects ninjago, which matters on releases where Siso is the default.

### Parts of the product outside the top

- A build step that reads a sibling of the top (`../tools/` or the absolute path of
  `$MOUNT/tools/`) works on a local build: Soong's sandbox shows the rest of the file system
  read-only. Its cache entry depends on how the step names the file, so give every machine the
  same mount path (see below).
- Remote compile runs only steps whose inputs are all inside the top; other steps build locally.
- `OUT_DIR` must be relative and inside the top, or unset. On Android 15 and 16, soong_ui
  rewrites an absolute `OUT_DIR` to a sandbox path that the ABFS hooks cannot use outside the
  sandbox; an `OUT_DIR` outside the top does not work with Soong's sandbox on any release.
- Builds of the other parts (`other/` in the example) read their sources from the mount lazily,
  like any program, but they do not use the ABFS build cache.

### Cache hits across machines

- Mount at the same path on every machine and for every user (for example `/abfs/ws` on every
  CI runner). Soong on Android 15 and 16 writes absolute links to some host tools into its
  outputs, so a different mount path changes those outputs and the steps that read them.
- Keep the build metadata pins of `vendorsetup.sh`: build with the environment that
  `source build/envsetup.sh` sets in the top, or set the same four `BUILD_*` values yourself.

## CI recipe

1. Push the product's manifest project and its projects with git-pusher, as in the
   [sysadmin guide](README.md#create-required-hashpaths). Name every manifest file your builds
   use under `files:`. A layout needs no pusher setting of its own.
2. Set the client config centrally, with `sync.auto-setup: true`, the manifest server and
   project of the product, and `setup.tops` only if detection does not find your tops:

    ```sh
    abfs edit --add --stdin config:clients/configs << 'EOF'
    - select:
      config:
        manifest-scheme: https
        manifest-server: gerrit.example.com
        manifest-project-name: product/manifest
        sync.auto-setup: true
        force-workspaces: true
    EOF
    ```

   A client reads `config:clients/configs` again every 10 seconds, and `abfs edit` can return
   before the server has the new config, so a mount that starts right after the edit can still
   use the old settings. Run this step ahead of the CI jobs, and before the first job after a
   change, check on the runner that `abfs config --remote` shows the new settings.

3. Once per runner, create the mount point and give it to the user that runs the CI jobs. ABFS
   runs as that user, who usually cannot create a directory under `/`:

    ```sh
    sudo install -d -o <ci-user> /abfs/ci
    ```

4. In each job, mount at that path, check the tops, and build:

    ```sh
    MOUNT=/abfs/ci
    abfs mount --tmp-ws --branch main $MOUNT
    abfs setup --list $MOUNT
    cd $MOUNT/android
    source build/envsetup.sh
    lunch <target>
    m
    ```

   `--list` must show each top with a `.ninjago.conf` and no missing binaries.
