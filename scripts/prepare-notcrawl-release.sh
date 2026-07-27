#!/usr/bin/env bash
set -euo pipefail

echo "local release publication is disabled; run: gh workflow run release-unified.yml --repo openclaw/notcrawl -f version=X.Y.Z" >&2
exit 1
