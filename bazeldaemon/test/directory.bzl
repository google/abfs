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

def _impl(ctx):
    out = ctx.actions.declare_directory(ctx.label.name)
    ctx.actions.run_shell(
        outputs = [out],
        command = """ls {} >$@""".format(out.path),
    )
    return DefaultInfo(files = depset([out]))

directory_rule = rule(
    implementation = _impl,
)

def _create_directory_impl(ctx):
    out = ctx.actions.declare_directory(ctx.label.name)
    ctx.actions.run_shell(
        outputs = [out],
        command = """mkdir -p {}; touch {}/.config""".format(out.path, out.path),
    )
    return DefaultInfo(files = depset([out]))

create_directory_rule = rule(
    implementation = _create_directory_impl,
)