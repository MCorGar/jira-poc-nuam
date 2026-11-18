#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p .tools
if [[ "$(uname -s)" != Linux || "$(uname -m)" != x86_64 ]]; then
  echo "Este instalador corresponde a Linux amd64. Instala k3d para tu arquitectura." >&2
  exit 1
fi
curl --location --fail --silent --show-error \
  https://github.com/k3d-io/k3d/releases/download/v5.9.0/k3d-linux-amd64 \
  --output .tools/k3d
echo '06d8f25bc3a971c4eb29e0ff08429b180402db0f4dec838c9eac427e296800a0  .tools/k3d' | sha256sum --check
chmod +x .tools/k3d
.tools/k3d version
