# Core operating contract

## Profiles and availability

- `--profile <name>` overrides `TALENTO_PROFILE`, which overrides the configured default.
- A profile is one OAuth grant for one company. Never combine identifiers, previews, or results
  between profiles.
- `talento commands --available --agent` is the capability check. Missing tools reflect live account,
  module, role, permission, visibility, or tenant rules. Explain the limit; do not suggest bypassing it.

## Read the result state before speaking

Every generated or raw tool call returns a state. Treat it as the contract:

- `committed`: Talento reports a persisted change. Report what actually changed.
- `preview`: nothing was executed. Present the preview and wait for explicit user authorization.
  Then run `talento action confirm <preview-id> --agent`, or repeat the original command with `--yes`
  only when the user already authorized that exact preview-producing command.
- `submitted_for_approval`: a request was filed, but approval may still be pending. Say submitted or
  pending, never approved or completed.
- `returned`: the server returned information without asserting persistence. Describe the result,
  not an inferred side effect.
- structured error: nothing succeeded. Preserve the message and resolve ambiguity or permissions.

Never re-run a write merely to confirm its preview. Re-running it creates or returns a new action.
Preview IDs expire and belong to the profile that created them.

## Names, ambiguity, and inputs

- Prefer the human-readable name flags exposed by the command. Never ask the user for a database ID.
- Zero matches: say none were found. Multiple matches: show the distinguishing names/context and ask
  which one. Do not pick silently.
- For nested values, pass reviewed JSON through `--input`, an individual JSON-valued flag, or
  `--input-file`. Do not interpolate untrusted text into shell syntax; use an argument-safe runner.
- Use `--jq` only to select existing data. It must not become a source of business calculations.

## Scope and accuracy

- Choose `mine`, `team`, `office`, or `all` from the user's words. A named employee is a separate,
  explicit selector.
- Trust the applied scope, totals, statuses, and truncation messages returned by Talento.
- Do not recompute server totals or infer a company-wide result from a partial/truncated list.
- Interpret relative dates against the user's current date and pass ISO dates.

## Analysis

Cross-domain reports are an orchestration technique, not permission expansion. Combine only data the
profile can read, identify fuzzy name-based joins, surface partial coverage, and distinguish evidence
from inference. A trustworthy partial answer is better than an invented complete one.
