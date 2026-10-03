# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

def _ln_symlink_impl(ctx):
    target = ctx.file.target_file
    symlink_output = ctx.actions.declare_symlink("link_created_by_ln.txt")

    # ln -s must use a relative or absolute path
    ctx.actions.run_shell(
        inputs = [target],
        outputs = [symlink_output],
        command = 'ln -s ' + target.path + ' ' + symlink_output.path,
    )

    return DefaultInfo(files = depset([symlink_output]))

ln_symlink_rule = rule(
    implementation = _ln_symlink_impl,
    attrs = {
        "target_file": attr.label(allow_single_file = True),
    },
)
