#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
EXPECTED='local release publication is disabled; run: gh workflow run release-unified.yml --repo openclaw/notcrawl -f version=X.Y.Z'

for script in \
  notarize-notcrawl-macos.sh \
  package-notcrawl-macos-release.sh \
  prepare-notcrawl-release.sh \
  verify-notcrawl-macos-release.sh \
  verify-notcrawl-release.sh; do
  bash -n "$ROOT/scripts/$script"
done

for script in prepare-notcrawl-release.sh package-notcrawl-macos-release.sh; do
  if output=$("$ROOT/scripts/$script" 2>&1); then
    echo "$script unexpectedly allowed local publication" >&2
    exit 1
  fi
  [[ "$output" == "$EXPECTED" ]] || {
    echo "$script did not print the official workflow command" >&2
    exit 1
  }
done

echo "release script tests passed"
