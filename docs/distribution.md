# Distribution and verification

GoReleaser builds `talento` for darwin/linux/windows on amd64 and arm64. Archives include the binary,
license, README, canonical skill, native plugins, and pre-generated Bash, Zsh, Fish, and PowerShell
completions. Linux deb, rpm, and apk packages install the corresponding shell completions in their
standard system locations. The release also attaches every supported agent wrapper. Homebrew,
Scoop, Nix, `go install`, shell, and PowerShell installation paths are supported.

## Release metadata contract

Git tags use `vVERSION`; binaries, archive names, plugin manifests, Nix, Homebrew, and Scoop use the
same normalized SemVer without the leading `v`. The tag is the source of truth for each release
workflow invocation. Before creating the tag, stamp and commit the Nix package version:

```sh
scripts/stamp-nix-version.sh VERSION
```

Plugin source manifests remain reusable templates. `cmd/packageextras` copies them into a staging
tree and stamps both Codex and Claude Code copies while building release archives, so a packaging
run never dirties the source manifests. GoReleaser uses only those staged plugin trees.

Before publication, `cmd/releaselockstep` opens every platform and standalone plugin archive and
checks its embedded manifest. It also executes the host release binary and checks the normalized
version, full commit, commit timestamp, and `release` source; checks the committed Nix stamp
and complete Nix build flags; and verifies the generated Homebrew and Scoop versions and tag URLs.
Any disagreement stops preview or stable publication before attestation.

Every release has `checksums.txt`, an Ed25519 `checksums.txt.sig` consumed by `talento upgrade`, a
keyless Sigstore bundle, per-archive SPDX SBOMs, and GitHub artifact attestations. The release public
key is embedded in the binary at build time; stable release automation refuses an unset or mismatched
key. Consumers can additionally run:

```sh
cosign verify-blob \
  --certificate-identity 'https://github.com/talentohq/talento-cli/.github/workflows/release.yml@refs/tags/v<version>' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  --bundle checksums.txt.sigstore.json checksums.txt

gh attestation verify talento_<version>_<os>_<arch>.<archive> -R talentohq/talento-cli
```

The direct shell and PowerShell installers require `cosign` and pin verification to the exact
selected release tag and the GitHub Actions OIDC issuer. They never treat a checksum downloaded
beside an archive as an independent trust root. Each installer validates archive layout, extracts
only the top-level executable, checks the candidate's exact version, and replaces an existing direct
installation transactionally with rollback. Stable Windows installation adds an exact Authenticode
certificate-subject check. The stable release environment must define
`TALENTO_WINDOWS_AUTHENTICODE_PUBLISHER` to the subject reported by `Get-AuthenticodeSignature` for
the protected signing certificate using printable ASCII; packaging stamps that value into the released `install.ps1`.
The repository copy intentionally fails closed for stable versions until it has been release-stamped.

Package-manager installations are not overwritten by `talento upgrade`; the command returns the
exact manager command. Direct archive installations download release metadata, verify the signed
manifest and artifact checksum, execute the downloaded binary to confirm its version, then swap the
executable with rollback.

Release discovery is channel-aware. A running v0 or SemVer-prerelease binary considers only
published GitHub prereleases, while a stable v1-or-newer binary considers only published,
non-prerelease stable releases. Drafts and malformed tags are ignored, and candidates are ordered by
SemVer rather than GitHub upload order. The shell and PowerShell installers default to `auto`, which
installs the newest stable release when one exists and otherwise falls back to the newest preview.
Set `TALENTO_CHANNEL=preview` or `TALENTO_CHANNEL=stable` to select a channel explicitly, or set
`TALENTO_VERSION` to install an exact release without discovery.

Release workflow routing uses SemVer precedence rather than a tag-prefix shortcut: every v0 release
and every tag with a prerelease suffix is published as a GitHub prerelease; only suffix-free v1 tags
enter the protected stable gates. Build metadata does not change that classification.
