#!/usr/bin/env bash
# Pull the latest jobs-app code and build the Go binaries natively on this
# machine (the NAS). The project is pure Go (modernc.org/sqlite, no cgo), so
# this needs only a Go toolchain - no cross-compile and no Docker.
#
# The browser-use worker is Python and is NOT portable: its venv must be rebuilt
# on this machine (see the remaining steps below and docs/browser-use-worker.md).
#
# Usage: ./scripts/deploy.sh
set -euo pipefail
cd "$(dirname "$0")/.."

targets=(server scrape classify batch)

echo "==> git pull"
git fetch --tags origin
git pull --ff-only origin HEAD

if ! command -v go >/dev/null 2>&1; then
  echo "ERROR: go is not installed. Install Go 1.27 on this machine first." >&2
  exit 1
fi

echo "==> build (native)"
for target in "${targets[@]}"; do
  echo "    -> bin/$target"
  go build -o "bin/$target" "./cmd/$target"
done

echo
echo "built: bin/{server,scrape,classify,batch}"
cat <<'EOF'
remaining manual steps:
  1. rebuild the Python worker venv on THIS machine (venvs are not portable):
       python3 -m venv .venv-browser
       .venv-browser/bin/pip install -r worker/requirements.txt
       .venv-browser/bin/playwright install chromium
  2. set the runtime env (server and scrape must share DB_PATH and DATA_DIR):
       DB_PATH, DATA_DIR, SERVER_ADDR, OLLAMA_HOST,
       BROWSER_WORKER_COMMAND=".venv-browser/bin/python worker/browser_worker.py",
       SCRAPE_COMMAND="<repo>/bin/scrape"
  3. classify every company before triggering any scrape:
       ./bin/classify -commit
  4. (re)start the server (systemd unit or however this machine runs it)
EOF
