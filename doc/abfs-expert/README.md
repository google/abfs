# ABFS Expert/Build Hygienist

This role is needed if you want ABFS builds to be fast and reliable.

At Google, we have a whole team of people who work to keep the Android build deterministic. If your internal fork of Android has broken incremental builds where you frequently need to run `m clean` then your build probably has some hidden dependencies or non-determinism. The upside is that ABFS prioritizes correctness when building so it will perform correct incremental builds without cleaning in between. The downside is that these hidden dependencies or non-determinism will slow down your builds because ABFS can’t re-use the build cache in these cases.

## ABFS Objects

ABFS uses its own CAS for storing all of its internal object types. This is nice because everything is stored and content-adressable. Many objects link to other objects using their SHA1 hash. Anytime you see a hex-encoded SHA1 hash, there are some CLI tools to help.

You can inspect objects:

```sh
# This is the empty file hash
HASH=e69de29bb2d1d6434b8b29ae775ad8c2e48c5391
abfs inspect $HASH
abfs i $HASH
```

If the object is a filesystem file or other raw blob, you can see the contents:

```sh
abfs cat $HASH
```

You can diff objects:

```sh
abfs diff $HASH1 $HASH2
```

You can retrieve the hash of a filesystem object within an ABFS mount:

```sh
abfs hash path/to/file_or_dir
```

You can set the hash of a filesystem object within an ABFS mount:

```sh
abfs hash --set $HASH /path/to/file_or_dir
```

These tools will be helpful when diagnosing build cache issues later.

## Finding Issues

### Repeating a Build

One way to find issues is to run the same build against the same source twice. Android’s `repo` tool supports pinned manifests to help here. Pinned manifests list a specific revision for each project so they will give a consistent snapshot of the source code. ABFS can use a pinned manifest to `mount` instead of using a moving target like a branch.

#### Pinned Manifest

You can generate a pinned manifest from the current state of a traditional repo checkout or an ABFS checkout:

```sh
repo manifest -r -o pinned.xml
```

Then you can use this file in future mounts to get a consistent state.

```sh
abfs mount --file pinned.xml $DIR --tmp-ws # --tmp-ws since we aren't changing code
```

#### Build

Once you have your mount, perform the usual `source`/`lunch`/`m` https://source.android.com/docs/setup/start#build_the_code for your chosen lunch target and build goals.

After your build is complete, you need to locate the file at `out/.ninja_log`. The file should start with `# ninjago log v1` otherwise your build did not use `ninjago` and it likely used traditional `ninja`.

ABFS workspaces don’t sync the `out/` directory to the server (and `--tmp-ws` doesn’t sync any state to the server), but the `.ninja_log` is stored as a blob on the server. If you have the stderr build log from your build, you should see a line like this near the end:

```
2026/10/06 22:40:14 Saved BuildInfo for 1791325959373725431 with hash 286a4664beb7174af43d8e693d1b28fb593a85ec
```

The SHA1 hash `286a4664beb7174af43d8e693d1b28fb593a85ec` is the key we need here.

You can also use `abfs list-builds` to see your recent builds.

Once you have a BuildInfo hash, you can use `abfs i` to find the ninja log.

```sh
abfs i $BUILD_HASH | grep NinjaLog
```

This will be a JSON excerpt like:

```
  "NinjaLog": "2ba25bb376c9fb5cc0b86d317e6d446e4acdf0df",
```

With this ninja log hash, we can now view it with something like:

```sh
abfs cat $NINJA_HASH | less
```

#### Compare

You only need to compare if you have 2 builds from the same exact source tree where the second build didn’t hit 100% cache hit rate. Two builds from the same machine in this scenario could find some non-determinism. Two builds from different machines in this scenario could find some environment differences.

We don’t have quality comparison tools in this area yet, so the process is still very manual.

##### Find a target that was rebuilt

On the **second** build’s ninja log, find a target that was rebuilt locally by searching for a line where the third column `Cached` has a value of `local`:

```
# ninjago log v1
#Timestamp    Duration Cached...
...
# look for a line like this:
1791328281862    5.958 local ...
```

That tells us that this build step or “Target” was rebuilt locally. If we expand this example line, we can see more info:

```
1791328281862    5.958 local          53f65356a53be013489d781db8eb5b67fc920a73 e2cab6a5fd17671ebd357ed4c7a2ea9b3d93ed83   2 out/soong/.intermediates/build/soong/android/soong-android/soong-android.a
```

Here is the data we care about in an easier format:

* Cached: `local` \- telling us this was rebuilt locally  
* Request-Hash: `53f65356a53be013489d781db8eb5b67fc920a73` \- this is the ABFS object encapsulating all of the info about the Target before it was executed including dependencies, environment variables, etc.  
* Response-Hash: `e2cab6a5fd17671ebd357ed4c7a2ea9b3d93ed83` \- this is the ABFS object encapsulating all of the info about the execution of the Target including actually read/written files, timing, etc.  
* Mismatch: `2` \- this shows that for this request-hash, the remote cache already had some response-hashes. We tried 2 of these and checked if their Read Patch (a list of all read files and directories, including hidden dependencies) matched the state of our local filesystem. Neither matched in this case so we proceeded to local execution.  
* Target: `out/soong/.intermediates/build/soong/android/soong-android/soong-android.a` \- this can be used to search in the first ninja log.

Once you find Target from the second build that was rebuilt locally, you can search the first build’s ninja log for this Target.

If the request-hashes are different, use those. If the response-hashes are different, use those. Diff the hash values to see what changed:

```sh
abfs diff $HASH1 $HASH2
```

For request-hash differences, there are often differences in the `EnvVars` (which can then be diff’ed). For response-hash differences, there are often differences in the `ReadPatch`, which often points to a hidden or non-deterministic dependency.
