#!/usr/bin/env bash
set -euo pipefail

BINARY=${1:-}

if [[ "$#" -ne 1 || -z "$BINARY" ]]; then
  echo "usage: $0 path-to-signed-notcrawl-binary" >&2
  exit 2
fi
[[ "$(uname -s)" == Darwin ]] || {
  echo "notcrawl notarization must run on macOS" >&2
  exit 1
}
[[ -f "$BINARY" ]] || {
  echo "notcrawl binary does not exist: $BINARY" >&2
  exit 1
}
[[ -n "${NOTARYTOOL_KEYCHAIN_PROFILE:-}" ]] || {
  echo "NOTARYTOOL_KEYCHAIN_PROFILE is required for notcrawl release notarization" >&2
  exit 1
}

for tool in codesign ditto mktemp plutil xcrun; do
  command -v "$tool" >/dev/null || {
    echo "missing required tool: $tool" >&2
    exit 1
  }
done

WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/notcrawl-notary.XXXXXX")
trap 'rm -rf "$WORK_DIR"' EXIT
SUBMISSION="$WORK_DIR/$(basename "$BINARY").zip"

ditto -c -k --sequesterRsrc --keepParent "$BINARY" "$SUBMISSION"
if ! NOTARY_RESULT=$(xcrun notarytool submit "$SUBMISSION" \
  --keychain-profile "$NOTARYTOOL_KEYCHAIN_PROFILE" \
  --no-s3-acceleration \
  --wait \
  --output-format json); then
  echo "notcrawl notarization submission failed: $BINARY" >&2
  exit 1
fi

NOTARY_STATUS=$(plutil -extract status raw -o - - <<<"$NOTARY_RESULT")
NOTARY_ID=$(plutil -extract id raw -o - - <<<"$NOTARY_RESULT")
[[ "$NOTARY_STATUS" == Accepted ]] || {
  echo "notcrawl notarization status is ${NOTARY_STATUS:-missing}, expected Accepted" >&2
  exit 1
}
[[ "$NOTARY_ID" =~ ^[[:xdigit:]]{8}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{12}$ ]] || {
  echo "notcrawl notarization response has an invalid submission id" >&2
  exit 1
}

codesign --verify --strict --check-notarization -R=notarized --verbose=2 "$BINARY"
printf 'notarized %s submission=%s\n' "$BINARY" "$NOTARY_ID"
