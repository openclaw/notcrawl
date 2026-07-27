#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TAG=${1:-}
ASSET_DIR=${2:-}

if [[ ! "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ || ! -d "$ASSET_DIR" ]]; then
  echo "usage: $0 vX.Y.Z asset-directory" >&2
  exit 2
fi

VERSION=${TAG#v}
BASE_VERSION=${VERSION%%-*}
BASE_VERSION_RE=${BASE_VERSION//./\\.}
ASSET_DIR=$(cd "$ASSET_DIR" && pwd)
EXPECTED_GLOBAL=$'notcrawl_'"${VERSION}"$'_darwin_amd64.tar.gz\nnotcrawl_'"${VERSION}"$'_darwin_arm64.tar.gz\nnotcrawl_'"${VERSION}"$'_linux_amd64.tar.gz\nnotcrawl_'"${VERSION}"$'_linux_arm64.tar.gz'
EXPECTED_CONTROLS=$'ASSET-INVENTORY.json\nRELEASE-NOTES.md\nSIGNING-MANIFEST.json'
EXPECTED_COMMIT=$(git -C "$ROOT" rev-parse "refs/tags/$TAG^{commit}" 2>/dev/null) || {
  echo "release tag does not exist locally: $TAG" >&2
  exit 1
}

for tool in bsdtar git go tar; do
  command -v "$tool" >/dev/null || {
    echo "missing required tool: $tool" >&2
    exit 1
  }
done

for path in \
  "$ASSET_DIR/notcrawl_${VERSION}_darwin_amd64.tar.gz" \
  "$ASSET_DIR/notcrawl_${VERSION}_darwin_arm64.tar.gz" \
  "$ASSET_DIR/notcrawl_${VERSION}_linux_amd64.tar.gz" \
  "$ASSET_DIR/notcrawl_${VERSION}_linux_arm64.tar.gz" \
  "$ASSET_DIR/sha256sums.txt"; do
  [[ -f "$path" ]] || {
    echo "missing release asset: $(basename "$path")" >&2
    exit 1
  }
done

deb_count=$(find "$ASSET_DIR" -maxdepth 1 -type f -name '*.deb' | wc -l | tr -d ' ')
rpm_count=$(find "$ASSET_DIR" -maxdepth 1 -type f -name '*.rpm' | wc -l | tr -d ' ')
[[ "$deb_count" -eq 2 && "$rpm_count" -eq 2 ]] || {
  echo "unexpected release package set: deb=$deb_count rpm=$rpm_count" >&2
  exit 1
}

package_names="$(
  find "$ASSET_DIR" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) \
    -exec basename {} \; | LC_ALL=C sort
)"
global_names="$(
  awk '{ name=$2; sub(/^\*/, "", name); print name }' "$ASSET_DIR/sha256sums.txt" | LC_ALL=C sort
)"
expected_names="$(printf '%s\n%s\n' "$EXPECTED_GLOBAL" "$package_names" | LC_ALL=C sort)"
expected_names="$(printf '%s\n%s\n' "$expected_names" "$EXPECTED_CONTROLS" | LC_ALL=C sort)"
[[ "$global_names" == "$expected_names" ]] || {
  echo "aggregate checksum manifest does not match release assets" >&2
  exit 1
}

expected_all_names="$(
  printf '%s\n' \
    "$expected_names" \
    'sha256sums.txt' | LC_ALL=C sort
)"
actual_names="$(
  find "$ASSET_DIR" -maxdepth 1 -type f -exec basename {} \; | LC_ALL=C sort
)"
[[ "$actual_names" == "$expected_all_names" ]] || {
  echo "release directory does not contain the exact 12-asset manifest" >&2
  diff <(printf '%s\n' "$expected_all_names") <(printf '%s\n' "$actual_names") >&2 || true
  exit 1
}

(
  cd "$ASSET_DIR"
  if command -v sha256sum >/dev/null; then
    sha256sum -c sha256sums.txt
  else
    shasum -a 256 -c sha256sums.txt
  fi
)

WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/notcrawl-linux-verify.XXXXXX")
trap 'rm -rf "$WORK_DIR"' EXIT

verify_go_provenance() {
  local binary=$1
  local expected_arch=$2
  local build_info

  build_info=$(go version -m "$binary")
  grep -F $'\tbuild\tvcs.revision='"$EXPECTED_COMMIT" <<<"$build_info" >/dev/null
  grep -F $'\tbuild\tvcs.modified=false' <<<"$build_info" >/dev/null
  grep -F $'\tbuild\tGOOS=linux' <<<"$build_info" >/dev/null
  grep -F $'\tbuild\tGOARCH='"$expected_arch" <<<"$build_info" >/dev/null
}

EXPECTED_LINUX_MEMBERS=$'CHANGELOG.md\nLICENSE\nREADME.md\nSPEC.md\nconfig.example.toml\nnotcrawl'
for arch in amd64 arm64; do
  archive="$ASSET_DIR/notcrawl_${VERSION}_linux_${arch}.tar.gz"
  stage="$WORK_DIR/archive-$arch"
  members=$(tar -tzf "$archive" | sed 's#^\./##' | sed '/^$/d' | LC_ALL=C sort)
  [[ "$members" == "$EXPECTED_LINUX_MEMBERS" ]] || {
    echo "unexpected archive contents in $(basename "$archive")" >&2
    exit 1
  }
  mkdir -p "$stage"
  tar -xzf "$archive" -C "$stage"
  verify_go_provenance "$stage/notcrawl" "$arch"
done

while IFS= read -r package; do
  name=$(basename "$package")
  stage="$WORK_DIR/package-$name"
  mkdir -p "$stage"
  if [[ "$name" == *.deb ]]; then
    [[ "$name" =~ ^notcrawl_${BASE_VERSION_RE}([^0-9].*)?_(amd64|arm64)\.deb$ ]] || {
      echo "unexpected Debian package name: $name" >&2
      exit 1
    }
  else
    [[ "$name" =~ ^notcrawl-${BASE_VERSION_RE}([^0-9].*)?\.(x86_64|aarch64)\.rpm$ ]] || {
      echo "unexpected RPM package name: $name" >&2
      exit 1
    }
  fi
  case "$name" in
    *_amd64.deb)
      arch=amd64
      bsdtar -xOf "$package" data.tar.gz | tar -xzf - -C "$stage" ./usr/bin/notcrawl
      binary="$stage/usr/bin/notcrawl"
      ;;
    *_arm64.deb)
      arch=arm64
      bsdtar -xOf "$package" data.tar.gz | tar -xzf - -C "$stage" ./usr/bin/notcrawl
      binary="$stage/usr/bin/notcrawl"
      ;;
    *.x86_64.rpm)
      arch=amd64
      binary="$stage/notcrawl"
      bsdtar -xOf "$package" /usr/bin/notcrawl >"$binary"
      ;;
    *.aarch64.rpm)
      arch=arm64
      binary="$stage/notcrawl"
      bsdtar -xOf "$package" /usr/bin/notcrawl >"$binary"
      ;;
    *)
      echo "unexpected package architecture: $name" >&2
      exit 1
      ;;
  esac
  verify_go_provenance "$binary" "$arch"
done < <(find "$ASSET_DIR" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) | LC_ALL=C sort)
