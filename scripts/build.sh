#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
export GOCACHE="$PWD/.runtime/go-cache"
export GOMODCACHE="$PWD/.runtime/go-mod"
export GOTOOLCHAIN=local
mkdir -p .runtime/bin
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o .runtime/bin/jira-poc ./cmd/jira-poc
