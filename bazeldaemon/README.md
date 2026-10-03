# Building using remote execution

1. Start the server:

   $ MNT=<ABFS mount point>
   $ go run ./cmd/abfs-bazel-daemon $MNT

2. Build hello:

   $ cd test
   $ bazel clean # force build
   $ bazel --batch  --digest_function=BLAKE3 build \
     --remote_executor grpc://localhost:5823 \
     --experimental_remote_output_service grpc://localhost:5823 \
     --verbose_failures \
     --experimental_remote_output_service_output_path_prefix=$MNT/output \
     hello
