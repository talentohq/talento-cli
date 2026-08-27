# Public CLI surface

The complete public Cobra API is an append-only, versioned contract under `.surface/`.
`versions/NNNN.json` captures every visible command and help topic, including command paths,
aliases, positional-argument syntax and validation, completion arguments, command groups,
runnable/deprecated state, and local, persistent, and inherited flags. Flag records include scope,
type, default, shorthand, no-option value, required marker, deprecation, and hidden state.

The snapshot deliberately excludes hidden commands. Making a visible command hidden therefore
looks like a removal and receives breaking-change review. Descriptions and examples are reviewed as
documentation, but are not machine-facing compatibility properties and are not included.

## Policy

- Additive commands and flags are compatible. They require a new reviewed snapshot, but no breaking
  approval.
- Removing or renaming a command, removing an alias, changing the positional-argument contract, or
  removing/changing an incumbent flag is breaking.
- A breaking change needs an exact entry in `.surface/breaking.json`. Each entry binds one transition
  and one change ID to before/after SHA-256 fingerprints and includes a human reason.
- Extra, stale, duplicate, broad, or fingerprint-mismatched approvals fail validation. One approval
  cannot mask another change.
- Existing snapshots are never rewritten. The index advances by exactly one version.

The repository began this history at version 1 using the complete post-hardening, post-onboarding
surface. There is no inferred pre-history from a Git tag.

## Review workflow

First inspect the exact drift without changing files:

```sh
go run ./cmd/surfacegen -diff
```

For an additive change, append the next snapshot directly. For a breaking change, copy each exact
change ID and its fingerprints from the diff into `.surface/breaking.json`, add a specific reason,
then append:

```sh
go run ./cmd/surfacegen -next
go run ./cmd/surfacegen -check
```

Review the new `versions/NNNN.json`, the one-line index advance, and any breaking approvals together.
CI runs only `-check`; it never mutates the checkout. `-init` exists solely to establish a history in
a repository that has none and refuses to replace an existing index.

## Help discovery

Root help groups setup, Talento work, raw MCP discovery, coding-agent integration, and maintenance.
Use these additional help topics for operational contracts:

```text
talento help output
talento help profiles
talento help writes
talento help exit-codes
talento help environment
talento help agents
```

`talento --agent --help` remains the concise machine-readable discovery format. The `.surface`
artifact is the more complete compatibility contract used during development and review.
