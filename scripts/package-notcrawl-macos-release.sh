#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TAG=${1:-}
OUT_DIR=${2:-"$ROOT/dist"}
IDENTIFIER=org.openclaw.notcrawl
EXPECTED_AUTHORITY='Developer ID Application: OpenClaw Foundation (FWJYW4S8P8)'
EXPECTED_TEAM_ID=FWJYW4S8P8
REQUIREMENT="identifier \"$IDENTIFIER\" and anchor apple generic and certificate 1[field.1.2.840.113635.100.6.2.6] exists and certificate leaf[field.1.2.840.113635.100.6.1.13] exists and certificate leaf[subject.OU] = \"$EXPECTED_TEAM_ID\""

usage() {
  echo "usage: $0 vX.Y.Z [output-directory]" >&2
  exit 2
}

[[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]] || usage
[[ "$(uname -s)" == Darwin ]] || {
  echo "notcrawl macOS release packaging must run on macOS" >&2
  exit 1
}
[[ "$(uname -m)" == arm64 ]] || {
  echo "notcrawl release packaging requires Apple Silicon with Rosetta" >&2
  exit 1
}
[[ -n "${CODESIGN_IDENTITY:-}" ]] || {
  echo "CODESIGN_IDENTITY is required; run this through mac-release codesign-run" >&2
  exit 1
}
[[ "$CODESIGN_IDENTITY" == "$EXPECTED_AUTHORITY" ]] || {
  echo "notcrawl releases require $EXPECTED_AUTHORITY" >&2
  exit 1
}

for tool in arch codesign git go lipo shasum tar; do
  command -v "$tool" >/dev/null || {
    echo "missing required tool: $tool" >&2
    exit 1
  }
done

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
  echo "release tag is not signed by a trusted git signing key: $TAG" >&2
  exit 1
}

mkdir -p "$OUT_DIR"
WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/notcrawl-release.XXXXXX")
trap 'rm -rf "$WORK_DIR"' EXIT
VERSION=${TAG#v}

for arch in arm64 amd64; do
  archive="$OUT_DIR/notcrawl_${VERSION}_darwin_${arch}.tar.gz"
  if [[ -e "$archive" || -e "$archive.sha256" ]]; then
    echo "refusing to overwrite existing artifact: $archive" >&2
    exit 1
  fi
done

for arch in arm64 amd64; do
  asset="notcrawl_${VERSION}_darwin_${arch}.tar.gz"
  archive="$OUT_DIR/$asset"
  stage="$WORK_DIR/$arch"
  binary="$stage/notcrawl"

  mkdir -p "$stage"
  (
    cd "$ROOT"
    CGO_ENABLED=0 GOOS=darwin GOARCH="$arch" GOWORK=off \
      go build -buildvcs=true -trimpath -ldflags "-s -w -X main.version=$VERSION" \
      -o "$binary" ./cmd/notcrawl
  )

  codesign --force --options runtime --timestamp \
    --identifier "$IDENTIFIER" --sign "$CODESIGN_IDENTITY" "$binary"
  codesign --verify --strict -R="$REQUIREMENT" --verbose=2 "$binary"

  signature=$(codesign -dvvv "$binary" 2>&1)
  grep -Fx "Identifier=$IDENTIFIER" <<<"$signature" >/dev/null
  grep -Fx "TeamIdentifier=$EXPECTED_TEAM_ID" <<<"$signature" >/dev/null
  grep -Fx "Authority=$EXPECTED_AUTHORITY" <<<"$signature" >/dev/null

  expected_arch=$arch
  [[ "$arch" == amd64 ]] && expected_arch=x86_64
  lipo -archs "$binary" | tr ' ' '\n' | grep -Fx "$expected_arch" >/dev/null
  if [[ "$arch" == amd64 ]]; then
    [[ "$(arch -x86_64 "$binary" --version)" == "$VERSION" ]]
  else
    [[ "$("$binary" --version)" == "$VERSION" ]]
  fi

  for file in README.md LICENSE SPEC.md config.example.toml; do
    cp "$ROOT/$file" "$stage/$file"
  done
  tar -czf "$archive" -C "$stage" notcrawl README.md LICENSE SPEC.md config.example.toml
  (
    cd "$OUT_DIR"
    shasum -a 256 "$asset" > "$asset.sha256"
  )
done

"$ROOT/scripts/verify-notcrawl-macos-release.sh" "$TAG" \
  "$OUT_DIR/notcrawl_${VERSION}_darwin_arm64.tar.gz" \
  "$OUT_DIR/notcrawl_${VERSION}_darwin_amd64.tar.gz"
