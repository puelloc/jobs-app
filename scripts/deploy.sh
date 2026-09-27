#!/usr/bin/env bash
# Update the jobs-app stack: pull latest, rebuild, rolling restart, then verify
# the API answers. Volumes carry state across upgrades; a broken build aborts
# before touching running containers (docker compose only recreates images it
# rebuilt OK).
set -euo pipefail
cd "$(dirname "$0")/.."

wget_quick=(--timeout=5 --tries=1)

# Print the service's recent logs inline: a remote report is only useful if it
# contains the actual error, and `docker compose ps` alone never does.
show_logs() {
  service="$1"
  lines="${2:-15}"
  echo "    ---- docker compose logs --tail $lines $service ----"
  docker compose logs --tail "$lines" "$service" 2>&1 | sed 's/^/    /' || true
  echo "    --------------------------------------------------"
}

# Counts the checks that indicate a real problem.
problems=0

echo "==> git pull"
git fetch --tags origin
git pull --ff-only origin HEAD

echo "==> data directory"
# The container runs as PUID/PGID and creates jobs.db in ./data. Docker would
# create a missing bind-mount source owned by root, which the container user
# cannot write, so create it here as the invoking user instead and compare
# ownership against PUID/PGID (not the invoking user).
export PUID="${PUID:-$(id -u)}"
export PGID="${PGID:-$(id -g)}"
mkdir -p data

owner_of() { stat -c '%u:%g' "$1" 2>/dev/null || stat -f '%u:%g' "$1" 2>/dev/null || echo '?'; }

if [ "$PUID:$PGID" != "$(id -u):$(id -g)" ]; then
  echo "    note: PUID/PGID=$PUID:$PGID are set in your environment; you are $(id -u):$(id -g)."
  echo "          The container runs as $PUID:$PGID and must own ./data."
  echo "          To run it as your own account instead:  unset PUID PGID"
fi

if [ "$(owner_of data)" != "$PUID:$PGID" ]; then
  echo "    ./data is owned by $(owner_of data), container runs as $PUID:$PGID - taking ownership"
  chown -R "$PUID:$PGID" data 2>/dev/null ||
    echo "    note: could not chown ./data (needs root)"
fi

data_ok=1
if [ "$(owner_of data)" != "$PUID:$PGID" ]; then
  data_ok=0
  echo "    ERROR: ./data is owned by $(owner_of data) but the container runs as $PUID:$PGID."
  echo "           The server will crash-loop with 'unable to open database file (14)'."
  echo "           Fix it with either of:"
  echo "             unset PUID PGID && ./scripts/deploy.sh   # run as your own account"
  echo "             sudo chown -R $PUID:$PGID data           # or match the container"
elif [ ! -w data ]; then
  data_ok=0
  echo "    ERROR: ./data is not writable by $(id -un)."
  echo "           Fix ownership, then re-run this script:  sudo chown -R $PUID:$PGID data"
else
  echo "    data/ is owned and writable by $PUID:$PGID"
fi

echo "==> rebuild + rolling restart"
# --remove-orphans clears containers from services that no longer exist.
docker compose up -d --build --remove-orphans

# Crash-looping containers are the failure mode a "successful" up can still leave
# behind, so say it loudly and point at the logs.
restarting=$(docker compose ps --format '{{.Service}} {{.State}}' 2>/dev/null \
  | awk '$2 == "restarting" { printf "%s ", $1 }' || true)
if [ -n "${restarting// /}" ]; then
  echo "    ERROR: restart-looping: ${restarting% }"
  show_logs app 25
  problems=$((problems + 1))
fi

echo "==> waiting for the API"
api_up=0
for _ in $(seq 1 60); do
  if docker compose exec -T app \
      wget -q "${wget_quick[@]}" -O- http://127.0.0.1:8080/api/runs >/dev/null 2>&1; then
    api_up=1
    break
  fi
  sleep 2
done
if [ "$api_up" = "1" ]; then
  echo "    server is answering"
else
  echo "    warning: server did not answer within 120s"
  show_logs app
  problems=$((problems + 1))
fi

echo "==> status"
docker compose ps

if [ "$data_ok" != "1" ]; then
  problems=$((problems + 1))
fi

if [ "$problems" -gt 0 ]; then
  echo
  echo "==> $problems check(s) need attention - see the warnings above"
  echo "    logs: docker compose logs --tail 100 app"
  exit 1
fi

echo
echo "==> post-deploy checks passed"
echo "    UI:   http://<nas-ip>:8095"
echo "    API:  http://<nas-ip>:8094/api/runs"
echo "    a fresh data volume needs, in order (or use the UI's Trigger buttons):"
echo "      docker compose run --rm app sp1500           # index companies"
echo "      docker compose run --rm app sp1500 resolve   # careers URLs"
echo "      docker compose run --rm app sp1500 validate  # browser-validate (optional)"
echo "      docker compose run --rm app classify -commit # vendor classification"
echo "      docker compose run --rm app batch            # full scrape sweep (sequential)"
