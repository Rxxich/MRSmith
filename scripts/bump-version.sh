#!/usr/bin/env bash
# Usage: scripts/bump-version.sh 1.0.4
# Writes the version to the backend (embedded into the binary) and the frontend.
set -euo pipefail
v="${1:?usage: bump-version.sh X.Y.Z}"
[[ "$v" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "version must look like 1.2.3" >&2; exit 1; }
root="$(cd "$(dirname "$0")/.." && pwd)"
printf '%s\n' "$v" > "$root/backend/internal/version/VERSION"
sed -i -E "0,/\"version\": \"[^\"]+\"/s//\"version\": \"$v\"/" "$root/frontend/package.json"
echo "version $v — add a CHANGELOG.md entry"
