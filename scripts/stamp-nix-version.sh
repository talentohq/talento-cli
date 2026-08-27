#!/bin/sh
set -eu

check=false
if [ "${1:-}" = "--check" ]; then
  check=true
  shift
fi

[ "$#" -eq 1 ] || {
  echo "Usage: scripts/stamp-nix-version.sh [--check] VERSION" >&2
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
current=$(sed -n 's/^"\([^"]*\)"$/\1/p' "$target")
[ -n "$current" ] || {
  echo "stamp-nix-version: $target has no version literal" >&2
  exit 1
}

if [ "$check" = true ]; then
  [ "$current" = "$version" ] || {
    echo "stamp-nix-version: $target is $current, expected $version" >&2
    echo "Run scripts/stamp-nix-version.sh $version and commit it before tagging." >&2
    exit 1
  }
  echo "$target is at $version"
  exit 0
fi

[ "$current" = "$version" ] && exit 0
temporary=$(mktemp "${target}.XXXXXX")
trap 'rm -f "$temporary"' EXIT HUP INT TERM
sed "s/^\"[^\"]*\"$/\"$version\"/" "$target" > "$temporary"
chmod 0644 "$temporary"
mv "$temporary" "$target"
trap - EXIT HUP INT TERM
