#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
[[ -x .runtime/bin/jira-poc ]] || bash scripts/build.sh
exec .runtime/bin/jira-poc evaluate "$@"
