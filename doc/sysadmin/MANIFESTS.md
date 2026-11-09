# Layered manifests

This page is for CI/CD and DevOps engineers whose manifest layers its own project lists over
AOSP with includes, include groups, `remove-project` and `extend-project`. ABFS applies such
a manifest by the rules of the
[`repo` tool](https://gerrit.googlesource.com/git-repo/+/HEAD/docs/manifest-format.md), on
both mount paths: the classic mount (`abfs mount -b`) and workspace mounts (`--ws`,
`--tmp-ws`, or any mount when the client config sets `force-workspaces: true`). A standard
AOSP manifest gives the same tree as before.

## How a mount reads the manifest

1. It reads the entry file of the manifest branch: on the classic mount the file that
   `--branch-file` names (default `default.xml`), on a workspace mount `default.xml` (see
   Mount).
2. It expands every `<include>`, nested includes too.
3. It adds local manifests: `--local-manifest-dir` on the classic mount,
   `.repo/local_manifests/*.xml` in name order on workspace mounts.
4. It applies `project`, `remove-project` and `extend-project` in document order and checks
   out the projects that remain.

| Element | What ABFS does |
|---|---|
| `<include name="f.xml" groups="g">` | Adds `g` to every `project` and `extend-project` in `f.xml` and in the files it includes. |
| `<include name="f.xml" revision="r">` | Sets `r` on every `project` and nested `include` in `f.xml` that has no `revision`. It takes precedence over the remote's `revision`. |
| `<remove-project name="n">` | Removes every project named `n`. |
| `<remove-project path="p">` | Removes the project at `p`, whatever its name. |
| `<remove-project name="n" path="p">` | Removes only the project named `n` at `p`. Other paths of `n` stay. |
| `remove-project optional="true"` | A remove that matches nothing is skipped. Without it, the mount fails. |
| `remove-project base-rev="r"` | The mount fails unless the removed project's revision is `r`. |
| `<extend-project name="n" path="p">` | Changes every project named `n`, or only the one at `p`. `revision`, `remote`, `dest-branch` and `upstream` replace the project's values, `groups` are added, `dest-path` moves the project, and `copyfile`, `linkfile` and `annotation` children are added. |
| `extend-project revision="r" base-rev="b"` | The mount fails unless the project's revision before the change is `b`. `base-rev` without `revision` checks nothing. |
| `extend-project remote="x"` | Changes the remote. The project keeps the revision it had, even when remote `x` has a `revision`. |

A project's revision is its own `revision`, else its remote's `revision`, else the
`<default>` revision.

## Rules a manifest must follow

The mount fails, as `repo init` and `repo sync` do, when:

- two projects sit at one path (`duplicate path`);
- an `extend-project` names a project that was never listed, or that a `remove-project`
  with only `name` removed (`extend-project element specifies non-existent project`);
- a `remove-project` without `optional="true"` matches nothing, or has neither `name` nor
  `path`;
- a `base-rev` does not match; the error lists every mismatch;
- an `extend-project` names a remote that the manifest does not define;
- an `extend-project` has `dest-path` but no `path` and the name sits at several paths.

ABFS also fails a `dest-path` that moves a project onto a path another project uses. The
`repo` tool accepts that manifest and then stops syncing the project that was at that path,
without a message.

## What is not applied

The mount prints a line starting with `Warning: manifest` for each of these and goes on
without them:

- a `<project>` nested in another `<project>`;
- a `<submanifest>`;
- an attribute ABFS does not read on `remote`, `default`, `project`, `extend-project`,
  `remove-project` or `include` (for example `rebase` or `sync-strategy`).

A group filter such as `-g local:my-file` prints a warning too: projects from local manifests
do not get the `local:` groups that `repo` gives them.

`<repo-hooks>` is kept in the manifest but no hook runs, so a `post-sync` hook that the
build needs has to run by hand after each sync.

## Seed the projects with git-pusher

List every entry file that your builds mount in the pusher pool config, each with its branch
(see [`gitpusher/README.md`](../../gitpusher/README.md)). The pusher follows includes:

```yaml
repos:
  - manifest:
      url: https://gerrit.example.com/platform/manifest
      files:
        - branch: release-1
          file: default.xml
        - branch: release-1
          file: partial.xml
```

The pusher applies `remove-project` and `extend-project` as the mount does. It mirrors the
projects that each listed file checks out, under the remote that an `extend-project` gives
them, so a mount finds each project under the host it looks up. A project that one file
removes is still mirrored when another listed file keeps it. A file that the mount rejects,
for example one whose `extend-project` names a project that no file defines, fails the
pusher's manifest read as well.

## Mount

Classic mount of a named entry file, with a group filter:

```sh
abfs mount -b release-1 --branch-file partial.xml -g example $MOUNT
```

`-g` selects projects whose groups come from the project, from an `<include groups=>` or
from an `extend-project`, as `repo init -g` does.

A workspace mount reads `default.xml` of the manifest branch, whatever `--branch-file`
names. For another entry file, keep a branch whose `default.xml` is that file, or flatten it
with the `repo` tool (`-g all` keeps every project in the file; `-g` on the mount selects) and
mount the result as a file:

```sh
repo init -u https://gerrit.example.com/platform/manifest -b release-1 -m partial.xml -g all
repo manifest -o flat.xml
abfs mount --ws ci -b release-1 -f flat.xml -g example $MOUNT
```

### Local manifests

- Classic mount: `abfs mount -b release-1 --local-manifest-dir $HOME/local-manifests $MOUNT`
  merges every `*.xml` file of that directory after the includes.
- Workspace mounts: put the files in `$MOUNT/.repo/local_manifests/`. Every `repo sync`
  (`abfs repo sync`) applies them in name order after the includes. An `<include>` inside a
  local manifest is relative to `.repo/`.

### Existing workspaces

The first `repo sync` (`abfs repo sync`) of a workspace made by an earlier ABFS version deletes
the checkouts that the manifest removes, or moves with `dest-path`; earlier versions checked
them out anyway. It deletes them without checking for local changes, while the `repo` tool
refuses to delete a checkout with uncommitted work. Commit and push, or copy, local changes in
those projects before that sync.

## Check a branch before CI mounts it

Compare the projects a workspace mount checked out with the list of the `repo` tool, run
with the same entry file and groups:

```sh
mkdir /tmp/repo-check && cd /tmp/repo-check
repo init -u https://gerrit.example.com/platform/manifest -b release-1 -g example
repo list -a -p | sort > /tmp/repo-paths.txt
sort $MOUNT/.repo/projects.list | diff /tmp/repo-paths.txt -
```

No output means the same project list. On a classic mount, check that every path in
`/tmp/repo-paths.txt` exists in the mount:

```sh
cd $MOUNT && while read -r p; do test -d "$p" || echo "missing: $p"; done < /tmp/repo-paths.txt
```

## Known limits

- `repo-hooks` do not run (see above).
- `repo sync` inside a workspace mount with an option that `abfs repo sync` does not accept
  (for example `-q`, `-v` or `--verify`) falls back to the `repo` tool's own network sync.
  Run `abfs repo sync`, or `repo sync` without those options.
- A workspace mount reads `default.xml` whatever `--branch-file` names (see Mount).
- A `<project revision="">` in a file included with `revision="r"` gets `r`. The `repo` tool
  treats the empty attribute as set and uses the remote's or the default revision. Leave the
  attribute out, or give the revision.
