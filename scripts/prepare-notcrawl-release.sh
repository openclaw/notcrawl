#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TAG=${1:-}
BUILD_DIR="$ROOT/dist"
OUT_DIR="$BUILD_DIR/release-assets"
HELPER=${MAC_RELEASE_HELPER:-"$HOME/Projects/agent-scripts/skills/release-mac-app/scripts/mac-release"}

if [[ ! "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ || "$#" -ne 1 ]]; then
  echo "usage: $0 vX.Y.Z" >&2
  exit 2
fi
[[ "$(uname -s)" == Darwin ]] || {
  echo "official release preparation must run on macOS" >&2
  exit 1
}
[[ -x "$HELPER" ]] || {
  echo "managed release helper is unavailable: $HELPER" >&2
  exit 1
}

head_commit=$(git -C "$ROOT" rev-parse HEAD)
tag_commit=$(git -C "$ROOT" rev-parse "refs/tags/$TAG^{commit}" 2>/dev/null) || {
  echo "release tag does not exist locally: $TAG" >&2
  exit 1
}
[[ "$head_commit" == "$tag_commit" ]] || {
  echo "HEAD does not match release tag $TAG" >&2
  exit 1
}
[[ -z "$(git -C "$ROOT" status --porcelain --untracked-files=normal)" ]] || {
  echo "release checkout is not clean" >&2
  exit 1
}
git -C "$ROOT" tag -v "$TAG" >/dev/null 2>&1 || {
  echo "release tag signature verification failed: $TAG" >&2
  exit 1
}

(
  cd "$ROOT"
  GOWORK=off goreleaser release --clean --skip=publish \
    --config .goreleaser-release.yml
)

mkdir -p "$OUT_DIR"
linux_assets="$(
  find "$BUILD_DIR" -maxdepth 1 -type f \( \
    -name 'notcrawl_*_linux_*.tar.gz' -o \
    -name '*.deb' -o \
    -name '*.rpm' \
  \) -print | LC_ALL=C sort
)"
[[ "$(printf '%s\n' "$linux_assets" | grep -c .)" -eq 6 ]] || {
  echo "expected exactly six Linux release artifacts" >&2
  exit 1
}
while IFS= read -r asset; do
  cp "$asset" "$OUT_DIR/"
done <<<"$linux_assets"

(
  cd "$ROOT"
  "$HELPER" codesign-run -- \
    "$ROOT/scripts/package-notcrawl-macos-release.sh" "$TAG" "$OUT_DIR"
)

VERSION=${TAG#v}
assets="$(
  find "$OUT_DIR" -maxdepth 1 -type f \( \
    -name '*.tar.gz' -o \
    -name '*.deb' -o \
    -name '*.rpm' \
  \) -exec basename {} \; | LC_ALL=C sort
)"
test "$(printf '%s\n' "$assets" | grep -c .)" -eq 8
(
  cd "$OUT_DIR"
  printf '%s\n' "$assets" | xargs shasum -a 256 > sha256sums.txt
)

"$ROOT/scripts/verify-notcrawl-release.sh" "$TAG" "$OUT_DIR"
"$ROOT/scripts/verify-notcrawl-macos-release.sh" "$TAG" \
  "$OUT_DIR/notcrawl_${VERSION}_darwin_arm64.tar.gz" \
  "$OUT_DIR/notcrawl_${VERSION}_darwin_amd64.tar.gz"
