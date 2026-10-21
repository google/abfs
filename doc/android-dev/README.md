# ABFS / Android Developer

## First Run

### Prerequisites

* Ensure you have ABFS and CASFS installed from the main [README doc](../../README.md).
* Ensure that you have already set up your machine for Android development and installed the required packages, see https://source.android.com/docs/setup/start#install-packages.

### One-Time Setup

Your ABFS System Admin will provide you with the `SERVER` value that you need to use below and potentially other flags. The important part is knowing whether your client should use TLS (or not).

This `init` step will start a background ABFS cache manager (cacheman) as a systemd process on your machine.

<details>
<summary>With TLS and Certificate File</summary>

If your ABFS System Admin provided you with a certificate file, you can either add it to your system trust store or only configure it to be used by ABFS. Choose one of these options:

**Add .crt to system trust store**

```sh
sudo cp ca.crt /usr/local/share/ca-certificates/abfs-ca.crt
sudo update-ca-certificates
```

Then run the normal init command after providing the proper `SERVER` value.

```sh
SERVER=
abfs --remote-servers $SERVER:50051 init
```

**Use .crt only for abfs**

```sh
CRT_INSTALL_PATH=/etc/abfs/ca.crt
sudo install -D -m 644 ca.crt $CRT_INSTALL_PATH
```

Then run the init command providing both the `SERVER` and the `CRT_INSTALL_PATH`:

```sh
SERVER=
abfs --remote-servers $SERVER:50051 --root-ca-file $CRT_INSTALL_PATH init
```

</details>

<details>
<summary>With TLS but no Certificate File</summary>

If your ABFS System Admin instructs you to use TLS but you don't need to install a certificate, just provide the `SERVER` below and use this command:

```sh
SERVER=
abfs --remote-servers $SERVER:50051 init
```

</details>

<details>
<summary>Without TLS</summary>

If your ABFS System Admin instructs you to not use TLS, just provide the `SERVER` below and use this command:

```sh
SERVER=
abfs --remote-servers $SERVER:50051 --disable-tls init
```

</details>

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
