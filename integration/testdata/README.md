# Testdata for Integration tests

This directory contains test git repositories to use for abfs integration tests.
Each subdirectory can be created with standard git commands, but should have the
.git directory renamed to 'git' prior to commiting. In addition, the
subdirectory must contain a manifest file named default.xml and map the test
repository to the path "test".

The 'git' directory must also contain at least one tracked file under refs/
(git does not track empty directories, and a repository without a refs/
directory is not recognized). Commit a loose ref such as refs/heads/main that
matches packed-refs; do not use a non-ref placeholder file, since git treats
every file under refs/ as a ref and `git fsck` will fail.

