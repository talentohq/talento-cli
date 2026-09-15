# Changelog

## [1.0.5] - Unreleased

### Added

- `talento time now` maps `get_current_datetime` so agents use the company clock instead of guessing.
- Goal checklist commands: `talento goals list-actions`, `create-action`, `update-action`, `delete-action`.
- Training flags `--external`, `--external-url`, and `--skills` (optional proficiency per skill).

### Changed

- `talento tasks create-task` is project-only. Goal checklists use the new goals action commands.
- Agent skill: resolve relative dates with `talento time now --agent`; distinguish project tasks,
  goal actions, and personal todos.

### Breaking

- Removed unused `--response-format` from `talento time list-clock-ins`, `talento goals comments`,
  and `talento schedules list` (the live MCP tools no longer accept it).

## [1.0.4] - 2026-09-05

### Added

- Meeting question templates as `talento meetings list|create|update|delete` (1:1s and hiring
  interviews).
- `talento trainings return-to-draft` for moving a published course back to draft.
- `talento crm edit-crm-custom-field` and lead custom-field observation history
  (`talento leads record-custom-field-observation`, `talento leads list-custom-field-observations`).
- Additive CRM flags: `--position` and `--track-history` on custom-field create, plus `field_uuid`
  on `custom_fields` JSON input.

## [1.0.3] - 2026-09-02

### Fixed

- `talento skill install`, `update`, and `remove` printed a blank `Talento setup:` line instead of
  the managed files that were installed, updated, unchanged, or removed.

## [1.0.2] - 2026-09-02

### Added

- Grok Build TUI (`grok`) as a managed coding-agent integration. `talento skill install --agent grok`
  writes the canonical skill to `~/.grok/skills/talento` (user) or `.grok/skills/talento` (project).
  Releases attach `talento-grok-wrapper_<version>.zip`.

## [1.0.1] - 2026-09-02

### Fixed

- The verified `install.sh` accepts macOS/BSD tar members named `./talento` as well as `talento`.
  v1.0.0 macOS archives failed after Sigstore verification because the installer required the
  unprefixed name.

### Changed

- Homebrew 6 requires `brew trust talentohq/tap` before the cask will load. Homebrew casks are
  macOS-only; Linux users should use `install.sh` or the `.deb` / `.rpm` / `.apk` packages.

## [1.0.0] - 2026-09-02

### Added

- First stable release of the native TalentoHQ CLI: Developer ID signed and notarized macOS
  binaries, Linux archives and `deb` / `rpm` / `apk` packages, Homebrew cask, Nix, `go install`,
  and a Sigstore-verified `install.sh`.

Windows packages are withheld until Authenticode signing is available.
