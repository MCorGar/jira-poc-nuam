#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
export KUBECONFIG="$PWD/.runtime/kubeconfig.yaml"
exec kubectl --context k3d-jira-poc-nuam "$@"
