### Background

This document explains how to bring up an in-memory server and other components like git-pusher and
cacheman for testing and development purposes.

### Being up an in-memory server
You can bring up an in-memory server. By default it listens on port 50051. 

Remember that it is an in-memory server so it will not persist any data and if you restart it you need to restart the whole cluster configuration. To avoid the new server being tainted by the potential stale data from a previous run, it is recommended to remove all the previous process before starting a new one (or start the server on a different port).

```bash
abfs server in-memory
```

### Setup a client instance
Choose an ABFS_INSTANCE name and set it in your environment.

```bash
export ABFS_INSTANCE=dev

# Stop and remove any existing cacheman 
abfs cacheman stop
abfs instance rm -f $ABFS_INSTANCE

# Setup a new cacheman and point it to the in-memory server
mkdir -p ~/.abfs/$ABFS_INSTANCE
abfs init
abfs --remote-servers localhost:50051 config -w
abfs cacheman wait
```

### Being up a git-pusher
On a different terminal, you can bring up a git-pusher. With a minimum config of using a repo and a sample github project.

```bash
export ABFS_INSTANCE=dev

abfs edit -a config:

abfs edit -a config:clients/configs --stdin  <<EOF
- select:
  config:
    manifest-project-name: platform/manifest
    manifest-server: android.googlesource.com
EOF

abfs edit -a config:git-pusher/default --stdin <<EOF
pushers-count: 1
pushers:
 - name: $HOSTNAME

repos:
 - manifest:
     url: https://android.googlesource.com/platform/manifest
     files:
       - branch: main
         file: default.xml

projects:
 - url: https://github.com/actions/setup-go
EOF

# Remove the previous pusher left overs if any
rm -fr ~/git-pusher/

abfs git-pusher
```

### Mount and access source

Mount using repo:

```bash
export ABFS_INSTANCE=dev

mkdir -p mnt
abfs mount -b main mnt
# do your work
abfs unmount mnt
```


Mount and git clone independent project:

```bash
export ABFS_INSTANCE=dev

mkdir -p mnt
abfs casfs-mount mnt --daemon &

# Wait for the server to be ready
while [[ "$(abfs hash mnt)" != 4b825dc642cb6eb9a060e54bf8d69288fbee4904 ]]; do sleep 1; done

cd mnt
abfs git clone https://github.com/actions/setup-go
# do your work

cd
abfs unmount mnt -ki
```
