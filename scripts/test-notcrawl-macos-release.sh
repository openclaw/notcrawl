#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
EXPECTED_AUTHORITY='Developer ID Application: OpenClaw Foundation (FWJYW4S8P8)'

fail() {
  echo "macOS release script test failed: $*" >&2
  exit 1
}

for script in notarize-notcrawl-macos.sh package-notcrawl-macos-release.sh verify-notcrawl-macos-release.sh; do
  bash -n "$ROOT/scripts/$script"
done
# shellcheck disable=SC2016
grep -F '"$ROOT/scripts/notarize-notcrawl-macos.sh" "$binary"' \
  "$ROOT/scripts/package-notcrawl-macos-release.sh" >/dev/null
grep -F 'codesign --verify --strict --check-notarization -R=notarized' \
  "$ROOT/scripts/verify-notcrawl-macos-release.sh" >/dev/null

WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/notcrawl-notary-test.XXXXXX")
trap 'rm -rf "$WORK_DIR"' EXIT
FAKE_BIN="$WORK_DIR/bin"
mkdir -p "$FAKE_BIN"
export MOCK_CODESIGN_LOG="$WORK_DIR/codesign.log"
export MOCK_XCRUN_LOG="$WORK_DIR/xcrun.log"

cat >"$FAKE_BIN/uname" <<'EOF'
#!/usr/bin/env bash
case "${1:-}" in
  -s) echo Darwin ;;
  -m) echo arm64 ;;
  *) echo Darwin ;;
esac
EOF

cat >"$FAKE_BIN/ditto" <<'EOF'
#!/usr/bin/env bash
[[ "$#" -eq 6 && "$1" == -c && "$2" == -k && "$3" == --sequesterRsrc && "$4" == --keepParent ]]
[[ -f "$5" && "$6" == *.zip ]]
cp "$5" "$6"
EOF

cat >"$FAKE_BIN/xcrun" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"${MOCK_XCRUN_LOG:?}"
[[ "${1:-}" == notarytool && "${2:-}" == submit && -f "${3:-}" ]]
shift 3
profile=
no_s3=0
wait_for_result=0
json_output=0
while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --keychain-profile) profile=${2:-}; shift 2 ;;
    --no-s3-acceleration) no_s3=1; shift ;;
    --wait) wait_for_result=1; shift ;;
    --output-format) [[ "${2:-}" == json ]] && json_output=1; shift 2 ;;
    *) exit 2 ;;
  esac
done
[[ "$profile" == test-notary-profile && "$no_s3" == 1 && "$wait_for_result" == 1 && "$json_output" == 1 ]]
[[ "${MOCK_NOTARY_EXIT:-0}" == 0 ]] || exit 1
printf '{"id":"%s","status":"%s"}\n' \
  "${MOCK_NOTARY_ID:-01234567-89ab-cdef-0123-456789abcdef}" \
  "${MOCK_NOTARY_STATUS:-Accepted}"
EOF

cat >"$FAKE_BIN/plutil" <<'EOF'
#!/usr/bin/env bash
[[ "$#" -eq 6 && "$1" == -extract && "$3" == raw && "$4" == -o && "$5" == - && "$6" == - ]]
cat >/dev/null
case "$2" in
  status) printf '%s\n' "${MOCK_NOTARY_STATUS:-Accepted}" ;;
  id) printf '%s\n' "${MOCK_NOTARY_ID:-01234567-89ab-cdef-0123-456789abcdef}" ;;
  *) exit 2 ;;
esac
EOF

cat >"$FAKE_BIN/codesign" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"${MOCK_CODESIGN_LOG:?}"
if [[ " $* " == *' --check-notarization '* && "${MOCK_NOTARY_TICKET:-accepted}" != accepted ]]; then
  exit 1
fi
EOF

chmod +x "$FAKE_BIN"/*
BINARY="$WORK_DIR/notcrawl"
printf '#!/usr/bin/env bash\nexit 0\n' >"$BINARY"
chmod +x "$BINARY"
TEST_PATH="$FAKE_BIN:/usr/bin:/bin"

if PATH="$TEST_PATH" "$ROOT/scripts/notarize-notcrawl-macos.sh" "$BINARY" \
  >"$WORK_DIR/missing-profile.out" 2>"$WORK_DIR/missing-profile.err"; then
  fail "notarization accepted a missing keychain profile"
fi
grep -F 'NOTARYTOOL_KEYCHAIN_PROFILE is required' "$WORK_DIR/missing-profile.err" >/dev/null

if PATH="$TEST_PATH" CODESIGN_IDENTITY="$EXPECTED_AUTHORITY" \
  "$ROOT/scripts/package-notcrawl-macos-release.sh" v9.9.9 "$WORK_DIR/package" \
  >"$WORK_DIR/package-missing-profile.out" 2>"$WORK_DIR/package-missing-profile.err"; then
  fail "release packaging accepted a missing keychain profile"
fi
grep -F 'NOTARYTOOL_KEYCHAIN_PROFILE is required' "$WORK_DIR/package-missing-profile.err" >/dev/null

PATH="$TEST_PATH" NOTARYTOOL_KEYCHAIN_PROFILE=test-notary-profile \
  "$ROOT/scripts/notarize-notcrawl-macos.sh" "$BINARY" >"$WORK_DIR/accepted.out"
grep -F 'submission=01234567-89ab-cdef-0123-456789abcdef' "$WORK_DIR/accepted.out" >/dev/null
grep -F -- '--keychain-profile test-notary-profile' "$MOCK_XCRUN_LOG" >/dev/null
grep -F -- '--wait --output-format json' "$MOCK_XCRUN_LOG" >/dev/null
grep -F -- '--check-notarization -R=notarized' "$MOCK_CODESIGN_LOG" >/dev/null

if PATH="$TEST_PATH" NOTARYTOOL_KEYCHAIN_PROFILE=test-notary-profile MOCK_NOTARY_STATUS=Invalid \
  "$ROOT/scripts/notarize-notcrawl-macos.sh" "$BINARY" >/dev/null 2>"$WORK_DIR/invalid-status.err"; then
  fail "notarization accepted an invalid service status"
fi
grep -F 'status is Invalid, expected Accepted' "$WORK_DIR/invalid-status.err" >/dev/null

if PATH="$TEST_PATH" NOTARYTOOL_KEYCHAIN_PROFILE=test-notary-profile MOCK_NOTARY_TICKET=rejected \
  "$ROOT/scripts/notarize-notcrawl-macos.sh" "$BINARY" >/dev/null 2>"$WORK_DIR/rejected-ticket.err"; then
  fail "notarization accepted a missing local ticket assessment"
fi

echo "macOS release script tests passed"
