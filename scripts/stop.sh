#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
# Detiene sin eliminar el clúster ni sus volúmenes.
.tools/k3d cluster stop jira-poc-nuam
