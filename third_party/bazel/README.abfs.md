# bazel (Remote Output Service protos)

The Bazel Remote Output Service protocol, used by the ABFS Bazel daemon
(`bazeldaemon/`) to construct Bazel's output tree.

- Upstream: https://github.com/bazelbuild/bazel, `src/main/protobuf/`
- Upstream commit: `37dbdcf14d3691b956b16e02bda8e79e42a16f2e`
  (2024-08-12, the last upstream change to both files)
- License: Apache 2.0, see [LICENSE](LICENSE)

## Local modifications

None to the `.proto` files, which are byte-for-byte copies of upstream.
The `*.pb.go` files are generated from them into the Go package
`outputservice` at
`github.com/google/abfs/third_party/bazel/src/main/protobuf`, using protoc
`M` options instead of editing `go_package`.

## Regenerating

With protoc 3.21.12, protoc-gen-go v1.28.1 and protoc-gen-go-grpc 1.2.0 on
`PATH`, from `third_party/bazel/src/main/protobuf`:

```sh
MC=$(go env GOMODCACHE)
GA=$MC/github.com/googleapis/googleapis@v0.0.0-20250822171309-731d7f2ab6e4
RA=$MC/github.com/bazelbuild/remote-apis@v0.0.0-20260216160025-715b73f3f9e4
GP=github.com/google/abfs/third_party/bazel/src/main/protobuf
M1="Mbazel_output_service.proto=$GP;outputservice"
M2="Mbazel_output_service_rev2.proto=$GP;outputservice"
protoc -I . -I "$RA" -I "$GA" \
  --go_out=. --go_opt=paths=source_relative --go_opt="$M1" --go_opt="$M2" \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  --go-grpc_opt="$M1" --go-grpc_opt="$M2" \
  bazel_output_service.proto bazel_output_service_rev2.proto
goimports -w *.pb.go
```

The `googleapis` and `remote-apis` module versions only provide imported
`.proto` files; any recent version works (run `go mod download` for them
first).
