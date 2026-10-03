# dagsync

`dagsync` is a Go library designed to synchronize Directed Acyclic Graphs (DAGs) of objects to a remote service. It handles dependency resolution, concurrent uploads, retries, and maintenance of the object graph.

## Overview

The core component is the `DAGSync` struct, which orchestrates the upload process. It maintains a graph of nodes representing objects to be synced.

- **Automated Dependency Management**: Automatically discovers links between objects (e.g., a Tree object linking to Blobs or other Trees) and ensures dependencies are uploaded before the parent object.
- **Concurrency**: Uploads objects in parallel using a configurable number of workers.
- **Retries**: Automatically retries failed uploads with an exponential backoff.
- **Maintenance**: Cleans up placeholder nodes that are no longer needed.

## Usage

### Initialization

Create a new `DAGSync` instance by providing an `ABFSUpdater` (for writing to the remote) and an `ObjectAccessor` (for reading local objects).

```go
opts := &dagsync.DagSyncOpts{
    Updater:      myUpdater,
    Accessor:     myAccessor,
    WorkersCount: 10,
    // Optional callbacks
    OnPush: func(h entities.Hash, t string) {
        fmt.Printf("Pushed %s (%s)\n", h, t)
    },
}
d := dagsync.New(opts)
```

### Starting

Start the synchronization processing loop in a background goroutine. This method blocks until the context is cancelled.

```go
ctx, cancel := context.WithCancel(context.Background())
go d.Start(ctx)
```

### Adding Requests

You can request objects to be synced using various methods. `dagsync` will automatically handle their dependencies.

- `PutObject(hash)`: Syncs a generic object.
- `PutBlob(hash)`: Syncs a blob object.
- `PutTree(hash)`: Syncs a tree object.
- `AddMapResult(from, to)`: Records a mapping between two objects.
- `PutRefs(server, project, refs)`: Updates references on a server.
- `Phony(priority, objects, callback)`: Phony requests do not request any data to be synced, but provide features on existing requests and query the the remote server for status. A priority can be provided which requests dagsync to prioritize the provided objects (and their dependencies) over others. Also, a callback can be provided to be notified on success or failure. The actual running of the request queries the remote server to see if the provided objects have been synced.

All these methods return a `fmt.Stringer` request object immediately and process the upload asynchronously. The return value is unique for an object (e.g. putting the same object again will return the same string) and can be used to query the state of a request.

## Configuration

- `WorkersCount`: Number of concurrent upload workers.
- `FailCooldown`: Base duration to wait before retrying a failed node.
- `PlaceholderNodeTimeout`: Duration to keep "placeholder" nodes (nodes created as dependencies but not yet fully known) before cleaning them up if they remain orphaned.
- `MaintenanceRunInterval`: Interval at which the maintenance loop runs to clean up stale nodes.

## Architecture

`dagsync` uses a priority queue to manage the execution order of nodes. Nodes with satisfied dependencies are moved to a `QueuedState` and processed by workers. If a node fails, it enters a `FailedState` and is rescheduled.
