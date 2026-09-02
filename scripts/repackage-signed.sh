#!/bin/sh
set -eu

dist="$1"
signed="$2"
version="$3"

work=""
stage=""
new_archive=""

cleanup() {
  if [ -n "$work" ]; then
    rm -rf -- "$work"
  fi
  if [ -n "$stage" ]; then
    rm -rf -- "$stage"
  fi
}

trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

absolute_path() {
  path="$1"
  directory="$(dirname "$path")"
  filename="$(basename "$path")"

  (
    CDPATH= cd "$directory"
    printf '%s/%s\n' "$(pwd -P)" "$filename"
  )
}

prepare_archive() {
  archive="$(absolute_path "$1")"
  archive_directory="$(dirname "$archive")"
  work="$(mktemp -d)"
  stage="$(mktemp -d "$archive_directory/.repackage.XXXXXX")"
  new_archive="$stage/$(basename "$archive")"
}

replace_archive() {
  mv "$new_archive" "$archive"
  rm -rf -- "$work" "$stage"
  work=""
  stage=""
  new_archive=""
}

for arch in amd64 arm64; do
  prepare_archive "$dist/talento_${version}_darwin_${arch}.tar.gz"
  binary="$signed/darwin-${arch}/talento"
  tar -xzf "$archive" -C "$work"
  cp "$binary" "$work/talento"
  chmod 0755 "$work/talento"
  tar --sort=name --mtime='UTC 1970-01-01' --owner=0 --group=0 --numeric-owner -czf "$new_archive" -C "$work" .
  replace_archive
done
