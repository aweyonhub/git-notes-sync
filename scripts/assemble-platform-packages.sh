#!/usr/bin/env bash
# Assemble platform sub-packages from dist/ binaries and sync versions.
# Single version input: rewrites the meta package.json version +
# optionalDependencies, and every sub-package package.json version.
# Requires: node (for the JSON sync), dist/ binaries (run `make cross` first).
# Usage: scripts/assemble-platform-packages.sh <version> [dist-dir]  (repo root)
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION="${1:?usage: assemble-platform-packages.sh <version> [dist-dir]}"
DIST="${2:-dist}"

# platform triplets: npm platform | npm arch | goos | goarch | ext
declare -a TRIPLETS=(
  "linux x64 linux amd64"
  "linux arm64 linux arm64"
  "darwin x64 darwin amd64"
  "darwin arm64 darwin arm64"
  "win32 x64 windows amd64 .exe"
  "win32 arm64 windows arm64 .exe"
)

for t in "${TRIPLETS[@]}"; do
  set -- $t
  npm_os=$1; npm_arch=$2; goos=$3; goarch=$4; ext=${5:-}
  pkg="packages/cli-$npm_os-$npm_arch"
  bin="$DIST/gns-$goos-$goarch$ext"
  [ -f "$bin" ] || { echo "missing $bin (run: make cross)"; exit 1; }
  [ -f "$pkg/package.json" ] || { echo "missing $pkg/package.json"; exit 1; }
  mkdir -p "$pkg/bin"
  cp "$bin" "$pkg/bin/gns$ext"
  # version sync only — metadata (name/os/cpu/files) is maintained in git
  node -e '
    const fs = require("fs");
    const p = process.argv[1];
    const j = JSON.parse(fs.readFileSync(p, "utf8"));
    j.version = process.argv[2];
    fs.writeFileSync(p, JSON.stringify(j, null, 2) + "\n");
  ' "$pkg/package.json" "$VERSION"
  echo "assembled $pkg (v$VERSION)"
done

# sync meta package: version + optionalDependencies
node -e '
  const fs = require("fs");
  const v = process.argv[1];
  const j = JSON.parse(fs.readFileSync("package.json", "utf8"));
  j.version = v;
  for (const k of Object.keys(j.optionalDependencies || {})) {
    j.optionalDependencies[k] = v;
  }
  fs.writeFileSync("package.json", JSON.stringify(j, null, 2) + "\n");
' "$VERSION"
echo "meta package synced (v$VERSION)"
