#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
export GOCACHE="$PWD/.runtime/go-cache"
export GOMODCACHE="$PWD/.runtime/go-mod"
export GOTOOLCHAIN=local
if command -v gcc >/dev/null; then
  CGO_ENABLED=1 go test -race ./...
else
  echo 'Sin compilador C local: pruebas normales. Para race, usa el target test de Docker.'
  CGO_ENABLED=0 go test ./...
fi
