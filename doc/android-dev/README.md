# ABFS / Android Developer

## First Run

### Prerequisites

Ensure you have ABFS and CASFS installed from the main [README doc](../../README.md).

### One-Time Setup

Your ABFS System Admin will provide you with the `SERVER` value that you need to use below and potentially other flags. This `init` step will start a background ABFS cache manager (cacheman) as a systemd process on your machine.


```sh
SERVER=
abfs --remote-servers $SERVER:50051 --disable-tls init
```

If there are issues, you need to check `abfs cacheman tail` to debug and then can run `abfs cacheman restart` after attempting any fixes.

### Mount

This will create a workspace named `dev` and mount it onto your machine. Any source changes you make will be persisted to the remote ABFS cache so you can unmount safely and remount at a later time. Replace `android-latest-release` with your branch name.

```sh
MOUNT=mnt
mkdir $MOUNT
abfs mount --ws dev --branch android-latest-release $MOUNT
```

### Build

Replace the below commands with your desired `lunch` target and build goal.

```sh
cd $MOUNT
source build/envsetup.sh
lunch aosp_cf_x86_64_only_phone-aosp_current-userdebug
m droid
```

At the beginning of your build, you should ensure `SOONG_NINJA=ninjago` and this log line from ninjago are output:

```
# example `m` output
Running product configuration...
============================================
...
SOONG_NINJA=ninjago
...
============================================
...
ninja: Starting ABFS build with ninjago to build out/soong/build.aosp_cf_x86_64_only_phone.ninja... 
```

`ninjago` is required to use the remote cache of ABFS so you need to debug if it isn’t being used. You can manually set `SOONG_NINJA=ninjago` if the automated setup isn't working.

If your target has been built before, you should have some cache hits and see a line like this printed at the end of both ninja phases:

```
# example `m` output
...
warning: ninja: Evaluation took 1m25.840705899s, fail: 0, cache: hits: 192249, misses: 0, ratio: 100.000
...
```

### Sync

`repo` and `git` are expected to work normally. Try syncing:

```sh
repo sync
```

### Remount

Just to try out the normal flow, you can `unmount` and `remount`:

```sh
cd ..
abfs unmount $MOUNT
abfs remount --ws dev $MOUNT
```

Now your tree with all of your changes will be back. See the [Workspace docs](WORKSPACES.md) for more details.
