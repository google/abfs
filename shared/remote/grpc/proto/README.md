# Protobuf

Google recommends keeping Java Protos in a separate package from hand-written Java Source, so we have a similar setup here for our Go Protos.

## First time setup

Install the compiler

``` bash
apt install -y protobuf-compiler
```

Install the go plugins

``` bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.28
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.2
```

Update your PATH so that the protoc compiler can find the plugins:

``` bash
export PATH="$PATH:$(go env GOPATH)/bin"
```

This also needs to be in your path for all future proto regeneration so you might want to add it to your shell startup script with something like below.

``` bash
echo 'export PATH="$PATH:$(go env GOPATH)/bin"' >> ~/.bashrc
```

## Regenerate after a proto file change

Run this from this directory

``` bash
PROTO_FILE=*.proto
protoc --go_out=. --go_opt=paths=source_relative \
    --go-grpc_out=. --go-grpc_opt=paths=source_relative \
    --proto_path=. \
    $PROTO_FILE
```

## New Dependency

If you add a dependency to a third party package, you will need to modify the `protoc` command above. Something like below will need to be added that uses `go list` to read the directory and then passes that directory to `protoc` via the `proto_path` flag.

``` bash
# This enables google proto imports by finding the path of googleapis protos from go.mod
GOOGLEAPIS_DIR=$(go list -f '{{.Dir}}' -m github.com/googleapis/googleapis)
protoc ... --proto_path=$GOOGLEAPIS_DIR ...
```
