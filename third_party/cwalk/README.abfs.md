# cwalk (vendored)

This directory is a vendored copy of `github.com/iafan/cwalk`, a concurrent
version of `filepath.Walk`.

- Upstream: https://github.com/iafan/cwalk
- Upstream base commit: `586a8832a711021ca304ee0129a70b7daaeb5363`
  ("Add mod file (#8)", 2021-01-25)
- License: MIT, see [LICENSE](LICENSE)

ABFS uses it through
`replace github.com/iafan/cwalk => ./third_party/cwalk` in the top-level
`go.mod`. The upstream project has had no changes since 2021.

## Local modifications

One local patch is applied on top of the upstream commit. It changes
`cwalk.go` and adds `cwalk_test.go`:

- `filepath.SkipDir` returned by the walk function for a regular file now
  skips only that file. Upstream stopped processing the remaining entries
  of the parent directory, so `SkipDir` behaved like "skip parent".
- `processPath` now handles one path at a time: it calls the walk function
  for the path itself, and for directories queues each child as a separate
  job. Errors, including broken symlinks (nil `FileInfo`), are reported on
  the error channel for that path instead of being returned.
- The walk root is processed the same way as every other path. `Walk` and
  `WalkWithSymlinks` start from `"."` instead of `""`, and a root that is
  not a directory is no longer reported as `ErrNotDir`.
- `cwalk_test.go` adds a test for the `SkipDir` behavior.

`MIT-LICENSE.txt` is renamed to `LICENSE`, and the modified and new Go files
carry a short copyright and SPDX header. No other files were changed. The
`METADATA` file records the upstream source and version.
