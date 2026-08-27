#!/bin/sh
set -eu
LC_ALL=C
export LC_ALL

repository="talentohq/talento-cli"
install_dir="${TALENTO_INSTALL_DIR:-/usr/local/bin}"
version="${TALENTO_VERSION:-}"
channel="${TALENTO_CHANNEL:-auto}"
max_download_bytes=268435456
max_binary_bytes=268435456

fail() {
  echo "talento installer: $*" >&2
  exit 1
}

download() {
  curl --fail --silent --show-error --location \
    --proto '=https' --proto-redir '=https' \
    --connect-timeout 10 --max-time 300 \
    --retry 3 --retry-delay 1 --max-filesize "$max_download_bytes" \
    "$@"
}

valid_version() {
  printf '%s\n' "$1" | awk '
    function valid_identifiers(value, numeric_no_leading_zero, identifiers, count, item_index) {
      if (value == "") return 0
      count = split(value, identifiers, ".")
      for (item_index = 1; item_index <= count; item_index++) {
        if (identifiers[item_index] == "" || identifiers[item_index] !~ /^[0-9A-Za-z-]+$/) return 0
        if (numeric_no_leading_zero && identifiers[item_index] ~ /^[0-9]+$/ && length(identifiers[item_index]) > 1 && substr(identifiers[item_index], 1, 1) == "0") return 0
      }
      return 1
    }
    {
      raw = $0
      copy = raw
      if (gsub(/\+/, "", copy) > 1) exit 1
      plus = index(raw, "+")
      if (plus > 0) {
        if (!valid_identifiers(substr(raw, plus + 1), 0)) exit 1
        raw = substr(raw, 1, plus - 1)
      }
      dash = index(raw, "-")
      if (dash > 0) {
        if (!valid_identifiers(substr(raw, dash + 1), 1)) exit 1
        raw = substr(raw, 1, dash - 1)
      }
      count = split(raw, parts, ".")
      if (count != 3) exit 1
      for (item_index = 1; item_index <= count; item_index++) {
        if (parts[item_index] !~ /^[0-9]+$/ || (length(parts[item_index]) > 1 && substr(parts[item_index], 1, 1) == "0")) exit 1
      }
      exit 0
    }
  '
}

if [ -z "$version" ]; then
  case "$channel" in
    auto|preview|stable) ;;
    *) fail "TALENTO_CHANNEL must be auto, preview, or stable" ;;
  esac
  releases="$(download -H 'Accept: application/vnd.github+json' -H 'User-Agent: talento-installer' "https://api.github.com/repos/${repository}/releases?per_page=100")"
  version="$(printf '%s\n' "$releases" | awk -v channel="$channel" '
    function valid_identifiers(value, numeric_no_leading_zero, identifiers, count, item_index) {
      if (value == "") return 0
      count = split(value, identifiers, ".")
      for (item_index = 1; item_index <= count; item_index++) {
        if (identifiers[item_index] == "" || identifiers[item_index] !~ /^[0-9A-Za-z-]+$/) return 0
        if (numeric_no_leading_zero && identifiers[item_index] ~ /^[0-9]+$/ && length(identifiers[item_index]) > 1 && substr(identifiers[item_index], 1, 1) == "0") return 0
      }
      return 1
    }
    function valid_semver(tag, raw, copy, pluses, plus, build, dash, core, prerelease, parts, count, item_index) {
      if (substr(tag, 1, 1) != "v") return 0
      raw = substr(tag, 2)
      copy = raw
      pluses = gsub(/\+/, "", copy)
      if (pluses > 1) return 0
      plus = index(raw, "+")
      if (plus > 0) {
        build = substr(raw, plus + 1)
        raw = substr(raw, 1, plus - 1)
        if (!valid_identifiers(build, 0)) return 0
      }
      dash = index(raw, "-")
      if (dash > 0) {
        prerelease = substr(raw, dash + 1)
        raw = substr(raw, 1, dash - 1)
        if (!valid_identifiers(prerelease, 1)) return 0
      }
      count = split(raw, parts, ".")
      if (count != 3) return 0
      for (item_index = 1; item_index <= count; item_index++) {
        if (parts[item_index] !~ /^[0-9]+$/ || (length(parts[item_index]) > 1 && substr(parts[item_index], 1, 1) == "0")) return 0
      }
      return 1
    }
    function version_without_build(tag, raw, position) {
      raw = substr(tag, 2)
      position = index(raw, "+")
      return position > 0 ? substr(raw, 1, position - 1) : raw
    }
    function version_core(tag, raw, position) {
      raw = version_without_build(tag)
      position = index(raw, "-")
      return position > 0 ? substr(raw, 1, position - 1) : raw
    }
    function version_prerelease(tag, raw, position) {
      raw = version_without_build(tag)
      position = index(raw, "-")
      return position > 0 ? substr(raw, position + 1) : ""
    }
    function compare_numeric(left, right) {
      if (length(left) < length(right)) return -1
      if (length(left) > length(right)) return 1
      if ("x" left < "x" right) return -1
      if ("x" left > "x" right) return 1
      return 0
    }
    function compare_semver(left, right, left_core, right_core, left_parts, right_parts, item_index, comparison, left_pre, right_pre, left_ids, right_ids, left_count, right_count, limit, left_numeric, right_numeric) {
      left_core = version_core(left)
      right_core = version_core(right)
      split(left_core, left_parts, ".")
      split(right_core, right_parts, ".")
      for (item_index = 1; item_index <= 3; item_index++) {
        comparison = compare_numeric(left_parts[item_index], right_parts[item_index])
        if (comparison != 0) return comparison
      }
      left_pre = version_prerelease(left)
      right_pre = version_prerelease(right)
      if (left_pre == "" && right_pre == "") return 0
      if (left_pre == "") return 1
      if (right_pre == "") return -1
      left_count = split(left_pre, left_ids, ".")
      right_count = split(right_pre, right_ids, ".")
      limit = left_count < right_count ? left_count : right_count
      for (item_index = 1; item_index <= limit; item_index++) {
        if (left_ids[item_index] == right_ids[item_index]) continue
        left_numeric = left_ids[item_index] ~ /^[0-9]+$/
        right_numeric = right_ids[item_index] ~ /^[0-9]+$/
        if (left_numeric && right_numeric) return compare_numeric(left_ids[item_index], right_ids[item_index])
        if (left_numeric) return -1
        if (right_numeric) return 1
        return left_ids[item_index] < right_ids[item_index] ? -1 : 1
      }
      return left_count < right_count ? -1 : (left_count > right_count ? 1 : 0)
    }
    function consider(candidate, kind) {
      if (kind == "stable" && (best_stable == "" || compare_semver(candidate, best_stable) > 0)) best_stable = candidate
      if (kind == "preview" && (best_preview == "" || compare_semver(candidate, best_preview) > 0)) best_preview = candidate
    }
    function json_value(object, key, rest, marker, position) {
      marker = "\"" key "\""
      position = index(object, marker)
      if (position == 0) return ""
      rest = substr(object, position + length(marker))
      position = index(rest, ":")
      if (position == 0) return ""
      rest = substr(rest, position + 1)
      sub(/^[[:space:]]*/, "", rest)
      return rest
    }
    function json_string(object, key, rest, position) {
      rest = json_value(object, key)
      if (substr(rest, 1, 1) != "\"") return ""
      rest = substr(rest, 2)
      position = index(rest, "\"")
      return position > 0 ? substr(rest, 1, position - 1) : ""
    }
    function json_boolean(object, key, rest) {
      rest = json_value(object, key)
      if (substr(rest, 1, 4) == "true") return "true"
      if (substr(rest, 1, 5) == "false") return "false"
      return ""
    }
    function process_release(object, tag, draft, prerelease, core_parts) {
      tag = json_string(object, "tag_name")
      draft = json_boolean(object, "draft")
      prerelease = json_boolean(object, "prerelease")
      if (draft == "false" && valid_semver(tag)) {
        split(version_core(tag), core_parts, ".")
        if (prerelease == "false" && version_prerelease(tag) == "" && core_parts[1] != "0") consider(tag, "stable")
        if (prerelease == "true") consider(tag, "preview")
      }
    }
    { json = json $0 "\n" }
    END {
      depth = 0
      in_string = 0
      escaped = 0
      object_start = 0
      for (character_index = 1; character_index <= length(json); character_index++) {
        character = substr(json, character_index, 1)
        if (in_string) {
          if (escaped) escaped = 0
          else if (character == "\\") escaped = 1
          else if (character == "\"") in_string = 0
          continue
        }
        if (character == "\"") {
          in_string = 1
        } else if (character == "{") {
          depth++
          if (depth == 1) object_start = character_index
        } else if (character == "}") {
          if (depth == 1 && object_start > 0) process_release(substr(json, object_start, character_index - object_start + 1))
          depth--
        }
      }
      selected = channel == "stable" ? best_stable : (channel == "preview" ? best_preview : (best_stable != "" ? best_stable : best_preview))
      if (selected != "") print substr(selected, 2)
    }
  ')"
  [ -n "$version" ] || fail "No compatible $channel Talento CLI release is available"
fi
version="${version#v}"
valid_version "$version" || fail "TALENTO_VERSION must be a valid semantic version"

case "$(uname -s)" in
  Darwin) os="darwin" ;;
  Linux) os="linux" ;;
  *) fail "Unsupported operating system" ;;
esac

case "$(uname -m)" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) fail "Unsupported architecture" ;;
esac

asset="talento_${version}_${os}_${arch}.tar.gz"
base="https://github.com/${repository}/releases/download/v${version}"
work="$(mktemp -d)"
stage=""
backup=""
target=""
transaction_started=0
install_complete=0
had_original=0

cleanup() {
  status=$?
  trap - 0 INT TERM HUP
  if [ "$transaction_started" -eq 1 ] && [ "$install_complete" -eq 0 ]; then
    if [ "$had_original" -eq 1 ]; then
      if [ -n "$backup" ] && [ -e "$backup" ]; then
        [ -z "$target" ] || rm -f "$target"
        if mv "$backup" "$target"; then
          echo "talento installer: restored the previous executable after installation failed" >&2
        else
          echo "talento installer: automatic rollback failed; the previous executable remains at $backup" >&2
          backup=""
        fi
      fi
    else
      [ -z "$target" ] || rm -f "$target"
    fi
  fi
  [ -z "$stage" ] || rm -f "$stage"
  [ -z "$backup" ] || rm -f "$backup"
  rm -rf "$work"
  exit "$status"
}
trap cleanup 0
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

download "$base/$asset" -o "$work/$asset"
download "$base/checksums.txt" -o "$work/checksums.txt"
download "$base/checksums.txt.sigstore.json" -o "$work/checksums.txt.sigstore.json"

command -v cosign >/dev/null 2>&1 || fail "cosign is required to verify Talento release identity; install cosign and retry"
if ! cosign verify-blob \
  --certificate-identity "https://github.com/talentohq/talento-cli/.github/workflows/release.yml@refs/tags/v${version}" \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  --bundle "$work/checksums.txt.sigstore.json" "$work/checksums.txt"; then
  fail "Sigstore verification failed for checksums.txt"
fi

expected="$(awk -v asset="$asset" '
  ($2 == asset || $2 == "*" asset) { count++; value = tolower($1) }
  END { if (count == 1) print value; else exit 1 }
' "$work/checksums.txt")" || fail "Release checksum must contain exactly one entry for $asset"
case "$expected" in
  *[!0-9a-f]*|'') fail "Release checksum for $asset is not a lowercase SHA-256 digest" ;;
esac
[ "${#expected}" -eq 64 ] || fail "Release checksum for $asset is not a SHA-256 digest"
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$work/$asset" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  actual="$(shasum -a 256 "$work/$asset" | awk '{print $1}')"
else
  fail "sha256sum or shasum is required to verify the downloaded archive"
fi
[ "$expected" = "$actual" ] || fail "Checksum verification failed for $asset"

tar -tzf "$work/$asset" > "$work/archive-members.txt" || fail "Cannot inspect $asset"
awk '
  {
    name = $0
    if (name == "" || substr(name, 1, 1) == "/" || index(name, "\\") > 0) exit 1
    count = split(name, parts, "/")
    for (item_index = 1; item_index <= count; item_index++) if (parts[item_index] == "..") exit 1
    if (name == "talento") binary_count++
  }
  END { if (binary_count != 1) exit 1 }
' "$work/archive-members.txt" || fail "$asset has an unsafe layout or does not contain exactly one top-level talento executable"
tar -tvzf "$work/$asset" talento > "$work/archive-binary.txt" || fail "Cannot inspect the talento archive entry"
[ "$(wc -l < "$work/archive-binary.txt" | tr -d ' ')" = "1" ] || fail "$asset contains duplicate talento entries"
[ "$(cut -c 1 "$work/archive-binary.txt")" = "-" ] || fail "The talento archive entry must be a regular file, not a link or directory"
mkdir "$work/extract"
tar -xOzf "$work/$asset" talento | dd of="$work/extract/talento" bs=1024 count=262145 2>/dev/null || fail "Cannot extract the talento executable"
[ -f "$work/extract/talento" ] && [ ! -L "$work/extract/talento" ] || fail "The extracted talento executable is not a regular file"
binary_size="$(wc -c < "$work/extract/talento" | tr -d ' ')"
case "$binary_size" in *[!0-9]*|'') fail "Cannot determine the extracted executable size" ;; esac
[ "$binary_size" -gt 0 ] && [ "$binary_size" -le "$max_binary_bytes" ] || fail "The extracted executable is empty or exceeds the size limit"

mkdir -p "$install_dir"
target="$install_dir/talento"
[ ! -L "$target" ] || fail "Refusing to replace symlinked installation target $target"
[ ! -e "$target" ] || [ -f "$target" ] || fail "Refusing to replace non-file installation target $target"

stage="$(mktemp "$install_dir/.talento-install.XXXXXX")"
install -m 0755 "$work/extract/talento" "$stage"

validate_binary() {
  candidate_output="$("$1" --agent version)" || return 1
  reported="$(printf '%s\n' "$candidate_output" | sed -n 's/^[[:space:]]*"version":[[:space:]]*"\([^"]*\)"[,[:space:]]*$/\1/p')"
  [ "$reported" = "$version" ]
}

validate_binary "$stage" || fail "Downloaded executable did not report the expected version $version"

if [ -e "$target" ]; then
  had_original=1
  backup="$(mktemp "$install_dir/.talento-rollback.XXXXXX")"
  rm -f "$backup"
  transaction_started=1
  mv "$target" "$backup" || fail "Cannot preserve the existing executable for rollback"
else
  transaction_started=1
fi
mv "$stage" "$target" || fail "Cannot replace $target; the previous executable will be restored"
stage=""
validate_binary "$target" || fail "Installed executable failed its version check; the previous executable will be restored"
install_complete=1
[ -z "$backup" ] || rm -f "$backup"
backup=""

echo "Installed Talento CLI $version from release v$version at $target"
case ":${PATH:-}:" in
  *:"$install_dir":*) ;;
  *) echo "Add $install_dir to PATH to run talento from a new shell." ;;
esac
