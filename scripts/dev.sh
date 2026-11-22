#!/usr/bin/env bash
# Runs the panel locally with settings from .env.local (data in ./.localdata).
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
set -a
[ -f "$root/.env.local" ] && . "$root/.env.local"
set +a
export MRSMITH_DATA_DIR="${MRSMITH_DATA_DIR:-$root/.localdata}"
cd "$root/backend"
# Launchers may start us without the user's PATH; fall back to the default Go install.
command -v go >/dev/null 2>&1 || export PATH="$PATH:/c/Program Files/Go/bin:/usr/local/go/bin"
exec go run ./cmd/mrsmith serve
