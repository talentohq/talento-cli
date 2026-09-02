# Changelog

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
