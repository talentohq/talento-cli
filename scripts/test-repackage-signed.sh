#!/bin/sh
set -eu

repository="$(CDPATH= cd "$(dirname "$0")/.." && pwd -P)"
scratch="$(mktemp -d)"
trap 'rm -rf -- "$scratch"' EXIT HUP INT TERM

mkdir -p "$scratch/tar-capability-input"
if ! tar --sort=name --mtime='UTC 1970-01-01' --owner=0 --group=0 --numeric-owner -czf "$scratch/tar-capability.tar.gz" -C "$scratch/tar-capability-input" . 2>/dev/null; then
  echo "SKIP: repackage smoke test requires GNU tar"
  exit 0
fi
rm -rf -- "$scratch/tar-capability-input" "$scratch/tar-capability.tar.gz"

version="0.9.0-test"

create_fixture() {
  root="$1"
  dist="$root/dist"
  signed="$root/signed binaries"
  mkdir -p "$dist"

  for arch in amd64 arm64; do
    mkdir -p "$signed/darwin-$arch" "$signed/windows-$arch"
    printf 'signed darwin %s\n' "$arch" > "$signed/darwin-$arch/talento"
    printf 'signed windows %s\n' "$arch" > "$signed/windows-$arch/talento.exe"
    chmod 0755 "$signed/darwin-$arch/talento" "$signed/windows-$arch/talento.exe"

    fixture="$root/fixture-$arch"
    mkdir -p "$fixture"
    printf 'unsigned darwin %s\n' "$arch" > "$fixture/talento"
    tar -czf "$dist/talento_${version}_darwin_${arch}.tar.gz" -C "$fixture" talento
    rm -f "$fixture/talento"
    printf 'unsigned windows %s\n' "$arch" > "$fixture/talento.exe"
    (
      cd "$fixture"
      zip -X -q "$dist/talento_${version}_windows_${arch}.zip" talento.exe
    )
    rm -rf -- "$fixture"
  done
}

verify_fixture() {
  root="$1"
  dist="$root/dist"
  signed="$root/signed binaries"

  for arch in amd64 arm64; do
    unpack="$root/unpack-$arch"
    mkdir -p "$unpack/darwin" "$unpack/windows"
    tar -xzf "$dist/talento_${version}_darwin_${arch}.tar.gz" -C "$unpack/darwin"
    unzip -q "$dist/talento_${version}_windows_${arch}.zip" -d "$unpack/windows"
    cmp "$signed/darwin-$arch/talento" "$unpack/darwin/talento"
    cmp "$signed/windows-$arch/talento.exe" "$unpack/windows/talento.exe"
  done

  if find "$dist" -maxdepth 1 -name '.repackage.*' -print | grep -q .; then
    echo "temporary repackaging directories were not cleaned up" >&2
    exit 1
  fi
}

relative_root="$scratch/relative path"
create_fixture "$relative_root"
(
  cd "$relative_root"
  "$repository/scripts/repackage-signed.sh" dist "signed binaries" "$version"
)
verify_fixture "$relative_root"

absolute_root="$scratch/absolute path"
create_fixture "$absolute_root"
"$repository/scripts/repackage-signed.sh" "$absolute_root/dist" "$absolute_root/signed binaries" "$version"
verify_fixture "$absolute_root"

echo "repackage-signed smoke test passed"
