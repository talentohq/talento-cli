#!/bin/sh
set -eu

unformatted=$(find . -type f -name '*.go' ! -path './vendor/*' ! -path './.git/*' -exec gofmt -l {} +)
if [ -n "$unformatted" ]; then
  echo "Go files need formatting:" >&2
  printf '%s\n' "$unformatted" >&2
  exit 1
fi

echo "Go formatting passed."
