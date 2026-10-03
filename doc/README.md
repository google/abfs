# abfs

## Documentation

- [ABFS Guide](UserGuide.md) - Main user guide and setup instructions.
- [ABFS Repo and ABFS Git Concepts](abfs-repo-git.md) - Conceptual overview of `abfs repo` and `abfs git` architectures and optimization mechanisms.
- [ABFS Workspaces](abfs-workspaces.md) - Conceptual overview of workspaces, background synchronization, conflict handling, and the `abfs ws` command suite.

## Quick Start

* Build ABFS and its tools

```
// From project root
make
```

The binaries will install in the `GOBIN` directory. If you wish to change the
install location, set the `GOBIN` environment variable.

```
// From project root
GOBIN=/install/here/ make
```

It's important to note that the installation will set an extended attribute on
the casfs-mounter binary. The install must be done in a location where extended
attributes are supported. NFS does not support extended attributes.

Follow the instructions sent to stdout.
