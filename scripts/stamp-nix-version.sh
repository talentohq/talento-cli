#!/bin/sh
set -eu

# Stamps nix/version.nix. A release version (anything other than *-dev) also
# dates the leading ## [Unreleased] section in CHANGELOG.md. The release
# workflow runs --check and refuses a tag whose changelog has no dated notes.

check=false
if [ "${1:-}" = "--check" ]; then
  check=true
  shift
fi

[ "$#" -eq 1 ] || {
  echo "Usage: scripts/stamp-nix-version.sh [--check] VERSION" >&2
  echo "Release versions other than *-dev also date CHANGELOG.md." >&2
  exit 2
}

version=${1#v}
case "$version" in
  ''|*[!0-9A-Za-z.+-]*)
    echo "stamp-nix-version: invalid version: $1" >&2
    exit 2
    ;;
esac

target=nix/version.nix
changelog=CHANGELOG.md
current=$(sed -n 's/^"\([^"]*\)"$/\1/p' "$target")
[ -n "$current" ] || {
  echo "stamp-nix-version: $target has no version literal" >&2
  exit 1
}

is_dev=false
case "$version" in
  *-dev) is_dev=true ;;
esac

heading_prefix="## [$version] - "

has_unreleased() {
  awk '$0 == "## [Unreleased]" { found = 1 } END { exit found ? 0 : 1 }' "$changelog"
}

has_dated_heading() {
  awk -v prefix="$heading_prefix" '
    index($0, prefix) == 1 {
      rest = substr($0, length(prefix) + 1)
      if (rest ~ /^[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]$/) found = 1
    }
    END { exit found ? 0 : 1 }
  ' "$changelog"
}

section_has_bullet() {
  start=$1
  awk -v start="$start" '
    index($0, start) == 1 { in_section = 1; next }
    in_section && $0 ~ /^## / { in_section = 0 }
    in_section && $0 ~ /^- / { found = 1 }
    END { exit found ? 0 : 1 }
  ' "$changelog"
}

require_changelog() {
  [ -f "$changelog" ] || {
    echo "stamp-nix-version: $changelog is missing. Add ## [Unreleased] notes before stamping $version." >&2
    exit 1
  }
  if has_unreleased; then
    echo "stamp-nix-version: $changelog still has ## [Unreleased]. Date that section for $version before tagging." >&2
    exit 1
  fi
  has_dated_heading || {
    echo "stamp-nix-version: $changelog has no \"## [$version] - YYYY-MM-DD\" section." >&2
    exit 1
  }
  section_has_bullet "$heading_prefix" || {
    echo "stamp-nix-version: $changelog section ## [$version] has no notes." >&2
    exit 1
  }
}

date_changelog() {
  [ -f "$changelog" ] || {
    echo "stamp-nix-version: $changelog is missing. Add ## [Unreleased] notes before stamping $version." >&2
    exit 1
  }
  if has_dated_heading; then
    if has_unreleased; then
      echo "stamp-nix-version: $changelog has ## [$version] and ## [Unreleased]. Stamp the post-release -dev version before adding the next notes." >&2
      exit 1
    fi
    section_has_bullet "$heading_prefix" || {
      echo "stamp-nix-version: $changelog section ## [$version] has no notes." >&2
      exit 1
    }
    return 0
  fi
  if ! has_unreleased; then
    echo "stamp-nix-version: $changelog has no ## [Unreleased] section to date for $version." >&2
    exit 1
  fi
  section_has_bullet "## [Unreleased]" || {
    echo "stamp-nix-version: $changelog ## [Unreleased] has no notes to release as $version." >&2
    exit 1
  }
  heading="## [$version] - $(date +%Y-%m-%d)"
  temporary=$(mktemp "${changelog}.XXXXXX")
  awk -v heading="$heading" '
    !done && $0 == "## [Unreleased]" { print heading; done = 1; next }
    { print }
  ' "$changelog" > "$temporary"
  chmod 0644 "$temporary"
  mv "$temporary" "$changelog"
}

if [ "$check" = true ]; then
  [ "$current" = "$version" ] || {
    echo "stamp-nix-version: $target is $current, expected $version" >&2
    echo "Run scripts/stamp-nix-version.sh $version and commit it before tagging." >&2
    exit 1
  }
  if [ "$is_dev" = false ]; then
    require_changelog
  fi
  echo "$target is at $version"
  exit 0
fi

if [ "$is_dev" = false ]; then
  date_changelog
fi

[ "$current" = "$version" ] && exit 0
temporary=$(mktemp "${target}.XXXXXX")
trap 'rm -f "$temporary"' EXIT HUP INT TERM
sed "s/^\"[^\"]*\"$/\"$version\"/" "$target" > "$temporary"
chmod 0644 "$temporary"
mv "$temporary" "$target"
trap - EXIT HUP INT TERM
