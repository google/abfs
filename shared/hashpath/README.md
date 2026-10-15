# Hashpath

A hashpath is similar in idea to the git concepts of commit-ish and tree-ish.

A hashpath can be recursively resolved to a content-addressable filesystem object (dir or blob).

## Valid Hashpaths

This is at the top for reference, but continue reading to understand.

```
<blob-hash>
<dir-hash>
<dir-hash>:<relative-path>
<commit-hash>:<relative-path>
<ref>:<relative-path> # with inferred project and server
<ref@project>:<relative-path> # with inferred server
<ref@project@server>:<relative-path>
```

## Examples

### Directory Hash and Relative Path

The simplest application to think about is the example from the [main README's Hello World](../../README.md#hello-world).

If we have a directory hash `a4599dfba0b19ace39b924faf2f739b319086b40`, then this is the *hash* part of of a hashpath. This directory has a file inside of it named `hello1`, so this is our *path* part of a hashpath. Together we can use these as a hashpath:

```
a4599dfba0b19ace39b924faf2f739b319086b40:hello1
```

This hashpath can be used in commands like `abfs inspect`, `abfs cat`, etc.

It is a useful shortcut instead of running `abfs ls` on the directory hash, finding the correct file hash, and then copying the file hash into another command.

### Commit Hash and Relative Path

The above example works too if you have a commit hash instead of a directory hash. The root directory of the tree will be explored to look up the relative path.

```sh
COMMIT_HASH=
abfs cat $COMMIT_HASH:README.md
```

### Refs

#### Android

The real power of hashpaths comes with refs. The ABFS CAS Server supports git style refs where a ref can point to any CAS object. So in the context of Android, we could explore a project without even mounting a tree:

```sh
# form is <ref@project@server>:<relative-path>
abfs inspect android17-release@platform/art@android.googlesource.com:build/README.md
```

* ref: `android17-release`
* project: `platform/art`
* server: `android.googlesource.com`
* path: `build/README.md`

#### ABFS

ABFS has been moving many configurations to hashpaths. There is a feature in the code base to watch a hashpath and then update the running service dynamically. This allows services like cacheman, git-pusher, etc. to update dynamically without a restart.

The hashpath form used for ABFS is `<ref>:<relative-path>`. This uses the  server default of `default-server` and project default of `abfs-meta`. These were just chosen to match the pattern of Android having projects stored in servers.

The configuration is stored under the ref `config` by default. So you can run this to see the current various configurations:

```sh
# abfs ls lists recursively by default
abfs ls config:.
```

Example output:

```
drwxrwxrwx 134787da67683128e0705fe32852574009dd1a9d clients
-rw-r--r-- 93d8bbedd0a6480b106f7ebfd4474ca566aae047 clients/configs
drwxrwxrwx a96310e8f03d51b9e0402a927ce1d9aa49356116 git-pusher
-rw-r--r-- 1de2957aca5f2da59dfab588fa74f02a6c238f5f git-pusher/default
```

We have an interactive editor command to simplify editing these files. There isn't a great validation story right now, though, so care must be taken. Run:

```sh
abfs edit config:clients/configs
```

Or if you want to use scripting, you can pipe `abfs cat <hashpath>` to `yq` for YAML editing. Then you can pipe this into `abfs edit --stdin <hashpath>` to apply the change.

