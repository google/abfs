# ABFS / System Admin / GCP server

The GCP server is a CAS server that keeps its data in Google Cloud instead of a local directory, so several servers can serve the same data. Clients and git-pushers use it the same way as the file-based server in the [System Admin doc](README.md).

## Where the data goes

| Data | Service |
| --- | --- |
| Projects and refs | Spanner database (default name `cas`) |
| Object headers and objects up to 32 KiB | Bigtable table `objects` |
| Map results | Bigtable table `maps` |
| The first 1 MiB of larger objects | Bigtable table `chunks` |
| The rest of larger objects | Cloud Storage bucket, in objects of 10 MiB and then 1000 MiB |

## Limits

* Nothing is ever deleted: there is no garbage collection. Plan storage for growth.
* Some content is stored but never referenced. When an upload fails after the server tried to write the object row, the server cannot tell whether the row was written, so it keeps the content. Concurrent uploads of the same object keep the content of every upload, though only one is referenced. This content is left for a later orphan sweep, which ABFS does not have yet.
* Bigtable must be read and written through single-cluster routing: an instance with one cluster, or an app profile with single-cluster routing (`--bt-app-profile`). With multi-cluster routing a server can report objects another server has just written as missing: uploads that link them fail until the client retries, objects can be stored again, and map results can be missed. Refs are in Spanner and are not affected.
* Map results come back oldest first by the time each server added them, so with several servers the order of results added close together follows the servers' clock skew.
* `who-am-i`, `SHA256` and the `/mark` endpoint are not implemented.

## Resources

Create these with your usual tooling, in one region:

* A Spanner instance. The schema needs little capacity; the smallest instance size is enough to start.
* A Bigtable instance with one cluster (see Limits).
* A Cloud Storage bucket.

### IAM roles

| Who | Role | On |
| --- | --- | --- |
| Server service account | `roles/spanner.databaseUser` | the database |
| Server service account | `roles/bigtable.user` | the Bigtable instance |
| Server service account | `roles/storage.objectUser` | the bucket |
| Server service account, with `--links-content` | `roles/iam.serviceAccountTokenCreator` | the server service account itself, to sign URLs |
| Server service account, with `--gcp-client-metrics` | `roles/monitoring.metricWriter` | the project |
| Whoever runs `abfs server gcp init` | `roles/spanner.databaseAdmin`, `roles/bigtable.admin`, `roles/storage.objectViewer` | the instances and the bucket |

`abfs server gcp init` exports no client library metrics and needs no monitoring role.

`--links-content` also needs the IAM Service Account Credentials API (`iamcredentials.googleapis.com`) enabled in the project and, on a Compute Engine VM, the `cloud-platform` access scope, because the server signs URLs through that API with its own service account.

### Create the database and tables

`abfs server gcp init` creates the Spanner database and its schema and the Bigtable tables and column families when they are missing, and checks that the bucket exists. It keeps existing data, so run it again after each upgrade:

```sh
abfs server gcp init --project <project> --instance <spanner-instance> --bucket <bucket> [--db cas] [--bt-instance objects]
```

The server checks the same resources at startup and stops with an error that names the missing one.

## Run the server

Both commands take the same flags. `abfs server gcp` is a subcommand of the `abfs` binary; flags of `abfs server` go before `gcp`:

```sh
abfs server --port 50051 gcp --project <project> --instance <spanner-instance> --bucket <bucket>
```

`abfs-server` is a separate binary (`make abfs-server`) with one flat list of flags, as in earlier `abfs-server` releases:

```sh
abfs-server --port 50051 --project <project> --instance <spanner-instance> --bucket <bucket>
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--project` | | Project of the Spanner and Bigtable instances (required) |
| `--instance` | | Spanner instance (required) |
| `--db` | `cas` | Spanner database |
| `--bt-instance` | `objects` | Bigtable instance |
| `--bt-app-profile` | instance default | Bigtable app profile; it must use single-cluster routing |
| `--bt-conn-pool` | `50` | Bigtable gRPC connections |
| `--bucket` | | Cloud Storage bucket (required) |
| `--headers-cache` | `100000000` | Object headers cached per server; about 180 bytes each |
| `--contents-cache` | `1000000` | Object content entries cached per server; entries of objects up to 32 KiB hold the object, so they can take up to about 49 KB each |
| `--links-content` | off | Return signed Cloud Storage URLs, valid for 10 minutes, for objects larger than `--links-content-threshold`, so that clients download them from Cloud Storage directly. Clients then need access to `storage.googleapis.com`, and the server signs one URL per Cloud Storage object on every read. Without it, the server streams every object. |
| `--links-content-threshold` | `1048576` | Size in bytes above which `--links-content` returns URLs |
| `--gcp-client-metrics` | off | Export the built-in metrics of the Spanner, Bigtable and Cloud Storage client libraries to Cloud Monitoring |
| `--no-link-validation`, `--no-hash-validation` | off | As for the file-based server |
| `--http-host` | `localhost` | Address of the HTTP server (`--http-port`). Its endpoints, such as `/debug` and `/server/status`, have no authentication. |

The other flags (`--port`, TLS, `--auth-enabled`, `--acl-enabled`, logging) are the same as for the file-based server.

The server serves Prometheus metrics on `/metrics` of the HTTP server.

Several servers can share the same database, tables and bucket. Put them behind one address, for example a load balancer, and give clients that address in `--remote-servers`.

## Try it locally with emulators

This runs the server against the Spanner and Bigtable emulators and the Cloud Storage testbench in Docker, with no Google Cloud project.

Start the emulators:

```sh
docker run -d --rm --name abfs-spanner -p 127.0.0.1:9010:9010 -p 127.0.0.1:9020:9020 \
  gcr.io/google.com/cloudsdktool/google-cloud-cli:emulators \
  gcloud emulators spanner start --host-port=0.0.0.0:9010 --rest-port=9020
docker run -d --rm --name abfs-bigtable -p 127.0.0.1:8086:8086 \
  gcr.io/google.com/cloudsdktool/google-cloud-cli:emulators \
  gcloud beta emulators bigtable start --host-port=0.0.0.0:8086
docker run -d --rm --name abfs-gcs -p 127.0.0.1:9000:9000 -p 127.0.0.1:8888:8888 \
  gcr.io/cloud-devrel-public-resources/storage-testbench:latest
```

When they are up, create the Spanner instance and the bucket, start the testbench's gRPC port and point ABFS at the emulators:

```sh
curl -s -X POST localhost:9020/v1/projects/local-proj/instances \
  -d '{"instanceId": "abfs", "instance": {"config": "emulator-config", "displayName": "abfs", "nodeCount": 1}}'
curl -s -X POST -H "Content-Type: application/json" "localhost:9000/storage/v1/b?project=local-proj" -d '{"name": "abfs-chunks"}'
curl -s "localhost:9000/start_grpc?port=8888"
export SPANNER_EMULATOR_HOST=localhost:9010
export BIGTABLE_EMULATOR_HOST=localhost:8086
export STORAGE_EMULATOR_HOST_GRPC=localhost:8888
```

Create the database and tables and start the server:

```sh
abfs server gcp init --project local-proj --instance abfs --bucket abfs-chunks
abfs server --port 50051 gcp --project local-proj --instance abfs --bucket abfs-chunks
```

In another shell, check it:

```sh
abfs --remote-servers localhost:50051 --disable-tls --auth-type none test-client health-check
abfs --remote-servers localhost:50051 --disable-tls --auth-type none test-client headers 4b825dc642cb6eb9a060e54bf8d69288fbee4904
```

The second command prints `4b825dc642cb6eb9a060e54bf8d69288fbee4904 tree 0`. From here, follow the [System Admin doc](README.md) from "Configuration" on. Stop the emulators with `docker stop abfs-spanner abfs-bigtable abfs-gcs`; they keep no data.
