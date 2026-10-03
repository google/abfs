# rendezvous

Rendezvous (highest random weight) hashing, used by ABFS to shard work such
as Git pushers and remote server connections.

- Based on: https://github.com/tysonmote/rendezvous
  (2014 version, up to commit `84eb962` "Add MIT license.")
- License: MIT, see [LICENSE](LICENSE)

## Local modifications

- `rendezvous.go` is a modified version of the upstream `rendezvous.go`:
  - Scores use SHA-1 of key and node instead of CRC-32C, and are compared
    as byte strings.
  - The type is `Hasher` (upstream: `Hash`), with `Node`, `Key` and `Score`
    byte-slice types, and no internal locking.
  - `GetN` sorts an index slice instead of reordering the nodes.
- `LICENSE.md` is renamed to `LICENSE`, and `rendezvous.go` carries a short
  copyright and SPDX header. The `METADATA` file records the upstream source
  and version.

This package is part of the abfs Go module and is imported as
`github.com/google/abfs/third_party/rendezvous`.

The Google-written `Chasher` (which selects which of a stream of keys belong
to a given worker index out of a worker count) and its `chash` command-line
wrapper are not derived from upstream and live, under the Apache License 2.0,
in [`rendezvous/`](../../rendezvous) at the top of the repository.
