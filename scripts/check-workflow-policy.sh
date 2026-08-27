#!/bin/sh
set -eu

workflow_dir=${1:-.github/workflows}

if [ ! -d "$workflow_dir" ]; then
  echo "workflow policy: directory not found: $workflow_dir" >&2
  exit 1
fi

set --
for workflow in "$workflow_dir"/*.yml "$workflow_dir"/*.yaml; do
  [ -f "$workflow" ] || continue
  set -- "$@" "$workflow"
done

if [ "$#" -eq 0 ]; then
  echo "workflow policy: no YAML workflows found in $workflow_dir" >&2
  exit 1
fi

awk '
  function fail(message) {
    printf "%s:%d: workflow policy: %s\n", FILENAME, FNR, message > "/dev/stderr"
    failed = 1
  }

  function indentation(line, copy) {
    copy = line
    sub(/[^ ].*$/, "", copy)
    return length(copy)
  }

  function finish_checkout() {
    if (checkout_active && !checkout_safe) {
      printf "%s:%d: workflow policy: actions/checkout must set persist-credentials: false\n", checkout_file, checkout_line > "/dev/stderr"
      failed = 1
    }
    checkout_active = 0
    checkout_safe = 0
  }

  function finish_file() {
    finish_checkout()
    if (current_file != "" && !default_permissions) {
      printf "%s: workflow policy: top-level permissions must be {}\n", current_file > "/dev/stderr"
      failed = 1
    }
  }

  FNR == 1 {
    finish_file()
    current_file = FILENAME
    default_permissions = 0
  }

  /^permissions:[[:space:]]*\{\}[[:space:]]*$/ {
    default_permissions = 1
  }

  /^[[:space:]]*-[[:space:]]/ {
    if (checkout_active && indentation($0) <= checkout_indent) {
      finish_checkout()
    }
  }

  /^[[:space:]]*(-[[:space:]]+)?uses:[[:space:]]*/ {
    ref = $0
    sub(/^.*uses:[[:space:]]*/, "", ref)
    sub(/[[:space:]#].*$/, "", ref)

    if (ref ~ /^\.\//) {
      next
    }

    if (ref ~ /^docker:\/\//) {
      if (ref !~ /@sha256:[0-9a-f]{64}$/) {
        fail("docker action references must use an immutable sha256 digest")
      }
      if ($0 !~ /#[[:space:]]*[^[:space:]]/) {
        fail("docker action digest must have an adjacent image-version comment")
      }
      next
    }

    separator = index(ref, "@")
    action_path = substr(ref, 1, separator - 1)
    revision = substr(ref, separator + 1)
    if (separator == 0 || action_path !~ /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+(\/[A-Za-z0-9_.-]+)*$/ || revision !~ /^[0-9a-f]{40}$/) {
      fail("remote action references must use a full 40-character commit SHA")
    }
    if ($0 !~ /#[[:space:]]*v[0-9]+([.][0-9]+){0,2}([-.+][A-Za-z0-9.-]+)?[[:space:]]*$/) {
      fail("remote action SHA must have an adjacent version comment")
    }

    if (action_path == "actions/checkout") {
      checkout_active = 1
      checkout_safe = 0
      checkout_file = FILENAME
      checkout_line = FNR
      checkout_indent = indentation($0)
    }
  }

  checkout_active && /^[[:space:]]*persist-credentials:[[:space:]]*false[[:space:]]*$/ {
    checkout_safe = 1
  }

  /^[[:space:]]*(container|image):[[:space:]]*[^[:space:]#]+/ {
    image = $0
    sub(/^[[:space:]]*(container|image):[[:space:]]*/, "", image)
    sub(/[[:space:]#].*$/, "", image)
    if (image !~ /@sha256:[0-9a-f]{64}$/) {
      fail("container images must use an immutable sha256 digest")
    }
    if ($0 !~ /#[[:space:]]*[^[:space:]]/) {
      fail("container image digest must have an adjacent image-version comment")
    }
  }

  END {
    finish_file()
    exit failed
  }
' "$@"

echo "Workflow action, container, checkout credential, and default token policies passed."
