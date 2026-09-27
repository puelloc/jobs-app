#!/usr/bin/env bash
# Cross-compile the jobs-app Go binaries for the NAS. Pure Go (modernc.org/sqlite,
# no cgo), so the same machine builds every target without a C toolchain.
#
# This builds only the Go side. The browser-use worker is Python and is NOT
# portable: rebuild its venv on the NAS (worker/requirements.txt + `playwright
# install chromium`). See docs/browser-use-worker.md for the three env traps.
#
# Usage: ./scripts/build.sh [targets...]
#   no args -> all targets for linux/amd64 and linux/arm64
#   target   -> one of: server scrape classify batch
#
# Output: bin/<os>/<arch>/<target>
set -euo pipefail
cd "$(dirname "$0")/.."

os="${GOOS:-linux}"
archs="${GOARCHS:-amd64 arm64}"
if [ "$#" -eq 0 ]; then
  targets=(server scrape classify batch)
else
  targets=("$@")
fi

# In-repo caches because the default $HOME cache locations are not writable here.
export GOCACHE="${GOCACHE:-$PWD/.gocache}"
export GOPATH="${GOPATH:-$PWD/.gopath}"
export CGO_ENABLED=0

for arch in $archs; do
  for target in "${targets[@]}"; do
    out="bin/$os/$arch/$target"
    echo "==> $os/$arch/$target -> $out"
    GOOS="$os" GOARCH="$arch" go build -o "$out" "./cmd/$target"
  done
done

echo
echo "built: bin/$os/<arch>/{${targets[*]}}"
echo "the worker is Python - rebuild its venv on the NAS separately"
