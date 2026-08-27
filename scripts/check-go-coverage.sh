#!/bin/sh
set -eu

floor=${TALENTO_COVERAGE_FLOOR:-49.0}
if [ -n "${COVERAGE_PROFILE:-}" ]; then
  profile=$COVERAGE_PROFILE
  cleanup=false
else
  profile=$(mktemp "${TMPDIR:-/tmp}/talento-coverage.XXXXXX")
  cleanup=true
fi

if [ "$cleanup" = true ]; then
  trap 'rm -f "$profile"' 0 INT TERM HUP
fi

# ./... is the module package set: Go excludes vendor packages by definition.
# -coverpkg makes each package test contribute to one cross-package statement
# profile; the summary below de-duplicates blocks that occur in multiple runs.
go test -count=1 -covermode=atomic -coverpkg=./... -coverprofile="$profile" ./...

read_values=$(awk '
  NR == 1 { next }
  {
    block = $1
    if (!(block in statements)) statements[block] = $2
    if ($3 > 0) hit[block] = 1
  }
  END {
    for (block in statements) {
      total += statements[block]
      if (block in hit) covered += statements[block]
    }
    print covered, total
  }
' "$profile")
set -- $read_values
covered=${1:-0}
total=${2:-0}

if [ "$total" -eq 0 ]; then
  echo "could not read statement coverage from $profile" >&2
  exit 1
fi

actual=$(awk -v covered="$covered" -v total="$total" 'BEGIN { printf "%.6f", 100 * covered / total }')
if ! awk -v actual="$actual" -v floor="$floor" 'BEGIN { exit !(actual + 0 >= floor + 0) }'; then
  printf 'coverage %.3f%% (%d / %d statements) is below the %.3f%% floor\n' "$actual" "$covered" "$total" "$floor" >&2
  exit 1
fi

printf 'coverage %.3f%% (%d / %d statements) meets the %.3f%% floor\n' "$actual" "$covered" "$total" "$floor"
