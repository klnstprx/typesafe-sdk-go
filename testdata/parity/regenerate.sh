#!/bin/sh
# Regenerate python-0.7.2.json from a checkout of the Python SDK at the pinned
# commit (see PINNED_COMMIT in generate.py). Requires uv.
set -eu
repo_root=$(cd "$(dirname "$0")/../.." && pwd)
sdk=$(cd "${1:-$repo_root/../typesafe-sdk-python}" && pwd)
cd "$repo_root"
uv run --frozen --project "$sdk" python testdata/parity/generate.py "$sdk"
