# jobs-app: Go API + browser-use worker in one image.
#
# Two build stages, mirroring the rss-app layout: a pinned Go toolchain builds
# the cgo-free binaries, then a Debian+Python runtime carries the browser-use
# worker (Playwright + its pinned Chromium) alongside them. One image runs the
# long-lived `server`; `docker compose run` runs the one-off jobs (sp1500,
# classify, scrape, batch) against the same /data volume.

# ---- Go build ----
# Pinned to the toolchain required by go.mod (go 1.27.1); a floating tag could
# be older and make the image build download another toolchain.
FROM golang:1.27.1-bookworm AS build
WORKDIR /src
# go.sum is required: the module depends on modernc.org/sqlite.
COPY go.mod go.sum ./
# The module cache and the compiler's build cache are mounted, not baked into a layer, so they
# survive across builds. Without the build-cache mount every deploy recompiles the whole dependency
# graph from scratch - modernc.org/sqlite is a very large generated file - which is ~13 minutes on
# the NAS. With it, a code change recompiles only this repo's packages.
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/server ./cmd/server \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/scrape ./cmd/scrape \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/classify ./cmd/classify \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/batch ./cmd/batch \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/sp1500 ./cmd/sp1500 \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/scraper ./cmd/scraper \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/listings ./cmd/listings

# ---- runtime ----
FROM python:3.12-slim-bookworm
# wget backs the compose healthcheck. The Chromium system libraries come from
# `playwright install --with-deps` below, not a hand-maintained apt list.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates wget \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Worker venv with the pinned Playwright + browser-use, then the matching
# Chromium and its OS deps. PLAYWRIGHT_BROWSERS_PATH keeps the browser out of a
# per-user HOME so it is found at runtime under any PUID, and the venv stays at
# /venv (world-readable) so the same is true of the interpreter.
COPY worker/requirements.txt ./worker/
RUN python3 -m venv /venv \
 && /venv/bin/pip install --no-cache-dir -r worker/requirements.txt \
 && PLAYWRIGHT_BROWSERS_PATH=/ms-playwright /venv/bin/playwright install --with-deps chromium \
 && chmod -R a+rX /ms-playwright

# Worker scripts + vendor playbooks. cmd/scrape and cmd/classify resolve these
# relative to the working directory (/app), so the layout must match the repo.
# --chmod: the build context may carry them 0600 (a dev checkout), but the container
# runs as a non-root uid and must read them. Doing it at copy time avoids a separate
# recursive chmod layer, which was minutes of pure overhead under load for a handful
# of files.
COPY --chmod=0755 worker/ ./worker/

# Go binaries: `server` is the long-running entrypoint, the rest are one-off jobs
# run with `docker compose run --rm app <sp1500|scraper|classify|scrape|batch|listings>`.
COPY --from=build /out/ /usr/local/bin/

# A named user for the default PUID 1000; compose overrides the uid via `user:`.
# Browser scratch (config dir, HOME, XDG) lives in world-writable /tmp so it works
# for any PUID; /data is owned by PUID through update.sh, not here.
RUN useradd -m -u 1000 jobs && mkdir -p /data && chown jobs:jobs /data
USER jobs

ENV PATH="/venv/bin:${PATH}" \
    HOME=/tmp \
    XDG_CACHE_HOME=/tmp/.cache \
    XDG_CONFIG_HOME=/tmp/.config \
    PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
    BROWSER_USE_CONFIG_DIR=/tmp/.browseruse \
    BROWSER_USE_BROWSER_ARGS="--no-sandbox --disable-gpu --disable-dev-shm-usage" \
    ANONYMIZED_TELEMETRY=false \
    BROWSER_USE_VERSION_CHECK=false \
    DB_PATH=/data/jobs.db \
    DATA_DIR=/data

EXPOSE 8080
CMD ["server"]
