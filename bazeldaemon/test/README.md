# Building using remote execution

1. Build the server:
   $ cd <ABFS workspace>
   $ make

2. Start the server:
   $ ~/bin/abfs-bazel-daemon $HOME/android/mnt/chroot -b out/bazel-builds

3. Copy the example test:
   $ mkdir $CHROOT/hello
   $ cp <ABFS workspace>/bazeldaemon/test/* $CHROOT/hello

3. Build hello:
   $ CHROOT=<ABFS chroot>
   $ cd $CHROOT/hello
   $ $CHROOT/prebuilts/kernel-build-tools/bazel/linux-x86_64/bazel \
     --batch \
     --output_user_root=$CHROOT/out \
     --digest_function=GITSHA1 \
     build \
     --incompatible_sandbox_hermetic_tmp=false \
     --experimental_sandbox_async_tree_delete_idle_threads=0 \
     --remote_executor=grpc://localhost:5823 \
     --experimental_remote_output_service=grpc://localhost:5823 \
     --experimental_remote_output_service_output_path_prefix=$CHROOT/out \
     --verbose_failures \
	 hello
