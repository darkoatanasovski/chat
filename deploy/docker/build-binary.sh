#!/usr/bin/env bash
# Builds the `chat` binary on the HOST for the Docker server's architecture,
# so the prebuilt runtime image (Dockerfile.prebuilt) needs no in-container Go
# build. Used by `make up-prebuilt`.
set -euo pipefail
cd "$(dirname "$0")/../.."

# Match the binary arch to the Docker engine's, so the image runs. Fall back to
# the host arch if the Docker daemon isn't reachable. Trim whitespace/newlines
# (the CLI can emit warnings alongside the value).
# `|| true` so a down/unreachable Docker daemon doesn't abort under `set -e`
# + pipefail — we fall back to the host arch.
arch="$(docker version -f '{{.Server.Arch}}' 2>/dev/null | tr -d '[:space:]' || true)"
[ -z "$arch" ] && arch="$(uname -m)"
case "$arch" in
  x86_64 | amd64) goarch=amd64 ;;
  aarch64 | arm64) goarch=arm64 ;;
  *) echo "unknown docker arch '$arch'; defaulting to amd64" >&2; goarch=amd64 ;;
esac

echo "==> building chat for linux/$goarch"
CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" go build -o deploy/docker/chat ./cmd/chat
echo "==> wrote deploy/docker/chat ($(du -h deploy/docker/chat | cut -f1))"
