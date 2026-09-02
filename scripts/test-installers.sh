#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
installer=$root/install.sh
work=$(mktemp -d)
trap 'rm -rf "$work"' 0 INT TERM HUP
fixture=$work/fixture
stubs=$work/stubs
stubs_without_cosign=$work/stubs-without-cosign
install_dir=$work/bin
mkdir -p "$fixture" "$stubs" "$stubs_without_cosign" "$install_dir"

fail() {
  echo "installer test: $*" >&2
  exit 1
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

write_candidate() {
  destination=$1
  cat > "$destination" <<'SCRIPT'
#!/bin/sh
if [ "${TALENTO_TEST_FAIL_INSTALLED:-0}" = "1" ] && [ "${0##*/}" = "talento" ]; then
  exit 23
fi
if [ "${1:-}" = "--agent" ] && [ "${2:-}" = "version" ]; then
  printf '{\n  "version": "%s",\n  "commit": "fixture",\n  "date": "0",\n  "source": "release"\n}\n' "${TALENTO_FIXTURE_VERSION:-0.1.0}"
  exit 0
fi
if [ "${1:-}" = "version" ]; then
  printf 'talento %s fixture\n' "${TALENTO_FIXTURE_VERSION:-0.1.0}"
  exit 0
fi
exit 2
SCRIPT
  chmod 0755 "$destination"
}

write_original() {
  destination=$1
  cat > "$destination" <<'SCRIPT'
#!/bin/sh
if [ "${1:-}" = "--agent" ] && [ "${2:-}" = "version" ]; then
  printf '{"version":"0.0.9"}\n'
  exit 0
fi
printf 'original talento\n'
SCRIPT
  chmod 0755 "$destination"
}

make_archive() {
  archive_version=$1
  rm -rf "$work/package"
  mkdir "$work/package"
  write_candidate "$work/package/talento"
  tar -czf "$fixture/talento_${archive_version}_linux_amd64.tar.gz" -C "$work/package" talento
  checksum=$(sha256_file "$fixture/talento_${archive_version}_linux_amd64.tar.gz")
  printf '%s  %s\n' "$checksum" "talento_${archive_version}_linux_amd64.tar.gz" > "$fixture/checksums.txt"
  printf '%s\n' fixture-bundle > "$fixture/checksums.txt.sigstore.json"
}

make_archive 0.1.0

cat > "$stubs/curl" <<'SCRIPT'
#!/bin/sh
destination=
url=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) destination=$2; shift 2 ;;
    -H|--proto|--proto-redir|--connect-timeout|--max-time|--retry|--retry-delay|--max-filesize) shift 2 ;;
    --fail|--silent|--show-error|--location) shift ;;
    https://*) url=$1; shift ;;
    *) shift ;;
  esac
done
[ -n "$url" ] || exit 91
if [ -z "$destination" ]; then
  case "$url" in
    https://api.github.com/*) cat "$TALENTO_TEST_FIXTURE/releases.json"; exit 0 ;;
    *) exit 92 ;;
  esac
fi
cp "$TALENTO_TEST_FIXTURE/${url##*/}" "$destination"
SCRIPT

cat > "$stubs/uname" <<'SCRIPT'
#!/bin/sh
case "${1:-}" in
  -s) printf 'Linux\n' ;;
  -m) printf 'x86_64\n' ;;
  *) exit 2 ;;
esac
SCRIPT

cat > "$stubs/cosign" <<'SCRIPT'
#!/bin/sh
printf '%s\n' "$*" >> "$TALENTO_TEST_COSIGN_LOG"
exit "${TALENTO_TEST_COSIGN_EXIT:-0}"
SCRIPT
chmod 0755 "$stubs/curl" "$stubs/uname" "$stubs/cosign"
cp "$stubs/curl" "$stubs/uname" "$stubs_without_cosign/"

output=$work/output
errors=$work/errors
cosign_log=$work/cosign.log

run_installer() {
  runner=$1
  selected_path=$2
  : > "$output"
  : > "$errors"
  env \
    PATH="$selected_path:/usr/bin:/bin" \
    TALENTO_VERSION=0.1.0 \
    TALENTO_INSTALL_DIR="$install_dir" \
    TALENTO_TEST_FIXTURE="$fixture" \
    TALENTO_TEST_COSIGN_LOG="$cosign_log" \
    TALENTO_TEST_COSIGN_EXIT="${TALENTO_TEST_COSIGN_EXIT:-0}" \
    TALENTO_TEST_FAIL_INSTALLED="${TALENTO_TEST_FAIL_INSTALLED:-0}" \
    TALENTO_FIXTURE_VERSION=0.1.0 \
    "$runner" "$installer" > "$output" 2> "$errors"
}

: > "$cosign_log"
run_installer /bin/sh "$stubs" || fail "POSIX sh happy path failed: $(cat "$errors")"
[ -x "$install_dir/talento" ] || fail "happy path did not install an executable"
grep -F 'Installed Talento CLI 0.1.0 from release v0.1.0' "$output" >/dev/null || fail "installed-version guidance is missing"
grep -F "Add $install_dir to PATH" "$output" >/dev/null || fail "PATH guidance is missing"
grep -F -- '--certificate-identity https://github.com/talentohq/talento-cli/.github/workflows/release.yml@refs/tags/v0.1.0' "$cosign_log" >/dev/null || fail "Sigstore identity is not pinned to the exact selected tag"

run_installer /bin/bash "$stubs" || fail "Bash 3.2 happy-path replacement failed: $(cat "$errors")"

rm -rf "$work/package"
mkdir "$work/package"
write_candidate "$work/package/talento"
tar -czf "$fixture/talento_0.1.0_linux_amd64.tar.gz" -C "$work/package" .
checksum=$(sha256_file "$fixture/talento_0.1.0_linux_amd64.tar.gz")
printf '%s  %s\n' "$checksum" "talento_0.1.0_linux_amd64.tar.gz" > "$fixture/checksums.txt"
printf '%s\n' fixture-bundle > "$fixture/checksums.txt.sigstore.json"
run_installer /bin/sh "$stubs" || fail "dot-slash archive layout failed: $(cat "$errors")"
[ -x "$install_dir/talento" ] || fail "dot-slash archive did not install an executable"
make_archive 0.1.0

original_hash=$(sha256_file "$install_dir/talento")
TALENTO_TEST_COSIGN_EXIT=1
export TALENTO_TEST_COSIGN_EXIT
if run_installer /bin/sh "$stubs"; then fail "Sigstore mismatch unexpectedly installed"; fi
[ "$(sha256_file "$install_dir/talento")" = "$original_hash" ] || fail "Sigstore failure changed the installed executable"
unset TALENTO_TEST_COSIGN_EXIT

if run_installer /bin/sh "$stubs_without_cosign"; then fail "missing cosign unexpectedly installed"; fi
grep -F 'cosign is required' "$errors" >/dev/null || fail "missing-verifier error is not actionable"
[ "$(sha256_file "$install_dir/talento")" = "$original_hash" ] || fail "missing verifier changed the installed executable"

cp "$fixture/checksums.txt" "$work/checksums.good"
printf '%064d  %s\n' 0 talento_0.1.0_linux_amd64.tar.gz > "$fixture/checksums.txt"
if run_installer /bin/sh "$stubs"; then fail "checksum mismatch unexpectedly installed"; fi
mv "$work/checksums.good" "$fixture/checksums.txt"
[ "$(sha256_file "$install_dir/talento")" = "$original_hash" ] || fail "checksum failure changed the installed executable"

rm -rf "$work/package"
mkdir "$work/package"
ln -s /bin/sh "$work/package/talento"
tar -czf "$fixture/talento_0.1.0_linux_amd64.tar.gz" -C "$work/package" talento
checksum=$(sha256_file "$fixture/talento_0.1.0_linux_amd64.tar.gz")
printf '%s  %s\n' "$checksum" talento_0.1.0_linux_amd64.tar.gz > "$fixture/checksums.txt"
if run_installer /bin/sh "$stubs"; then fail "symlink archive unexpectedly installed"; fi
grep -F 'regular file' "$errors" >/dev/null || fail "link-archive error is not actionable"
[ "$(sha256_file "$install_dir/talento")" = "$original_hash" ] || fail "unsafe archive changed the installed executable"

rm -rf "$work/package"
mkdir -p "$work/package/dir"
write_candidate "$work/package/talento"
tar -czf "$fixture/talento_0.1.0_linux_amd64.tar.gz" -C "$work/package" dir/../talento
checksum=$(sha256_file "$fixture/talento_0.1.0_linux_amd64.tar.gz")
printf '%s  %s\n' "$checksum" talento_0.1.0_linux_amd64.tar.gz > "$fixture/checksums.txt"
if run_installer /bin/sh "$stubs"; then fail "traversal archive unexpectedly installed"; fi
grep -F 'unsafe layout' "$errors" >/dev/null || fail "traversal-archive error is not actionable"
[ "$(sha256_file "$install_dir/talento")" = "$original_hash" ] || fail "traversal archive changed the installed executable"

make_archive 0.1.0
write_original "$install_dir/talento"
original_hash=$(sha256_file "$install_dir/talento")
TALENTO_TEST_FAIL_INSTALLED=1
export TALENTO_TEST_FAIL_INSTALLED
if run_installer /bin/sh "$stubs"; then fail "post-install failure unexpectedly succeeded"; fi
unset TALENTO_TEST_FAIL_INSTALLED
[ "$(sha256_file "$install_dir/talento")" = "$original_hash" ] || fail "post-install failure did not restore the original executable"
grep -F 'restored the previous executable' "$errors" >/dev/null || fail "rollback was not reported"
if find "$install_dir" -maxdepth 1 -name '.talento-*' -print | grep . >/dev/null; then
  fail "installer left staging or rollback files behind"
fi

signal_stubs=$work/signal-stubs
mkdir "$signal_stubs"
cp "$stubs/curl" "$stubs/uname" "$stubs/cosign" "$signal_stubs/"
cat > "$signal_stubs/mv" <<'SCRIPT'
#!/bin/sh
source_path=$1
destination_path=$2
if [ "${source_path##*/}" = "talento" ]; then
  case "${destination_path##*/}" in
    .talento-rollback.*)
      /bin/mv "$source_path" "$destination_path"
      kill -TERM "$PPID"
      exit 0
      ;;
  esac
fi
exec /bin/mv "$@"
SCRIPT
chmod 0755 "$signal_stubs/mv"
original_hash=$(sha256_file "$install_dir/talento")
if run_installer /bin/sh "$signal_stubs"; then fail "signal during backup handoff unexpectedly succeeded"; fi
[ "$(sha256_file "$install_dir/talento")" = "$original_hash" ] || fail "signal during backup handoff did not restore the original executable"
if find "$install_dir" -maxdepth 1 -name '.talento-*' -print | grep . >/dev/null; then
  fail "signal rollback left staging or rollback files behind"
fi

if env PATH="$stubs:/usr/bin:/bin" TALENTO_VERSION='../bad' TALENTO_INSTALL_DIR="$install_dir" TALENTO_TEST_FIXTURE="$fixture" TALENTO_TEST_COSIGN_LOG="$cosign_log" /bin/sh "$installer" > "$output" 2> "$errors"; then
  fail "path-shaped TALENTO_VERSION unexpectedly passed"
fi
grep -F 'valid semantic version' "$errors" >/dev/null || fail "invalid-version error is not actionable"

make_archive 0.1.0-a
cat > "$fixture/releases.json" <<'JSON'
[
  {"tag_name":"v0.1.0-Z","draft":false,"prerelease":true},
  {"tag_name":"v0.1.0-a","draft":false,"prerelease":true}
]
JSON
: > "$cosign_log"
if ! (
  unset TALENTO_VERSION
  env \
    PATH="$stubs:/usr/bin:/bin" \
    TALENTO_CHANNEL=preview \
    TALENTO_INSTALL_DIR="$install_dir" \
    TALENTO_TEST_FIXTURE="$fixture" \
    TALENTO_TEST_COSIGN_LOG="$cosign_log" \
    TALENTO_TEST_COSIGN_EXIT=0 \
    TALENTO_TEST_FAIL_INSTALLED=0 \
    TALENTO_FIXTURE_VERSION=0.1.0-a \
    /bin/sh "$installer" > "$output" 2> "$errors"
); then
  fail "preview discovery failed: $(cat "$errors")"
fi
grep -F 'Installed Talento CLI 0.1.0-a from release v0.1.0-a' "$output" >/dev/null || fail "SemVer preview discovery did not select the ASCII-ordinal maximum"
grep -F 'refs/tags/v0.1.0-a' "$cosign_log" >/dev/null || fail "discovered preview did not pin Sigstore to the selected tag"

echo "Unix installer offline tests passed under POSIX sh and Bash $(/bin/bash -c 'printf %s "$BASH_VERSION"')."
