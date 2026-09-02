# Release runbook (1.0.0)

Operator checklist for a stable `v1.0.0` tag. Preview (`v0.x` / SemVer-prerelease) tags skip
Apple signing; this document is for suffix-free `v1.x` only.
Packaged Windows archives are not published. Live staging-role evidence is not required for this
first stable line.

See also [Release gates](release-gates.md), [Staging contracts](staging-contracts.md), and
[Distribution and verification](distribution.md).

## 1. Prerequisites

| Item | Purpose |
| --- | --- |
| Apple **Developer ID Application** certificate (`.p12`) + P12 password | macOS codesign |
| Apple ID + **app-specific password** + 10-character Team ID | notarization via `notarytool` |
| `APPLE_SIGNING_IDENTITY` string (exact `codesign -s` identity) | stamped into the stable environment |
| Ed25519 release keypair | `checksums.txt.sig` / embedded public key |
| Signed git tags enabled (GPG or SSH) | `gh release create --verify-tag` |
| Public repo `talentohq/homebrew-tap` with an initial `main` commit | Homebrew cask publish |
| Org admin access to `talentohq` / `talentohq/talento-cli` | environments, secrets, branch protection |

## 2. One-time GitHub setup

### Package-index repos and tap tokens

```bash
gh repo create talentohq/homebrew-tap --public --description "Homebrew tap for the Talento CLI" --gitignore ""
gh repo create talentohq/scoop-bucket --public --description "Scoop bucket for the Talento CLI"

# seed homebrew-tap
rm -rf /tmp/homebrew-tap && gh repo clone talentohq/homebrew-tap /tmp/homebrew-tap
printf '%s\n' '# talentohq/tap' '' 'brew tap talentohq/tap' 'brew install --cask talento' > /tmp/homebrew-tap/README.md
git -C /tmp/homebrew-tap add README.md
git -C /tmp/homebrew-tap commit -m "docs: initial tap readme"
git -C /tmp/homebrew-tap push -u origin HEAD:main
gh repo edit talentohq/homebrew-tap --default-branch main

# seed scoop-bucket
rm -rf /tmp/scoop-bucket && gh repo clone talentohq/scoop-bucket /tmp/scoop-bucket
printf '%s\n' '# talentohq Scoop bucket' '' 'scoop bucket add talentohq https://github.com/talentohq/scoop-bucket' 'scoop install talento' > /tmp/scoop-bucket/README.md
git -C /tmp/scoop-bucket add README.md
git -C /tmp/scoop-bucket commit -m "docs: initial bucket readme"
git -C /tmp/scoop-bucket push -u origin HEAD:main
gh repo edit talentohq/scoop-bucket --default-branch main
```

Create a fine-grained PAT (or GitHub App) with Contents read/write on `talentohq/homebrew-tap` and
`talentohq/scoop-bucket` only. Store it in a password manager. Then:

```bash
printf '%s' "$HOMEBREW_TAP_TOKEN" | gh secret set HOMEBREW_TAP_TOKEN -R talentohq/talento-cli
printf '%s' "$SCOOP_BUCKET_TOKEN" | gh secret set SCOOP_BUCKET_TOKEN -R talentohq/talento-cli
```

The release workflow committer identity is already `talentohq-release-bot` / `releases@talentohq.com`;
that is git config only and does not need a matching GitHub user.

### Actions environments

```bash
gh api --method PUT repos/talentohq/talento-cli/environments/preview-release --input - <<'EOF'
{"wait_timer":0,"prevent_self_review":false}
EOF

gh api --method PUT repos/talentohq/talento-cli/environments/stable-release-gates --input - <<'EOF'
{"wait_timer":0,"prevent_self_review":false}
EOF
```

In the GitHub UI for `stable-release-gates`:

- Add at least one **required reviewer**. If you are the only org admin, leave `prevent_self_review`
  false so you can approve your own 1.0.0 run; turn it on later if a second person joins.
- Restrict deployments to tags matching `v[1-9]*` if the UI offers tag rules; otherwise leave
  unrestricted and rely on the workflow `classify` job.

`preview-release` does not need required reviewers for 1.0.0, but create it so a later `v0.x` tag does
not fail on a missing environment.

### Branch protection on the default branch

The GitHub default branch is still `master` (the `main` rename was blocked). Protect that branch.
If the default is later renamed to `main`, retarget this call.

Do **not** require pull-request reviews on release day — that would block the version-stamp push.
Minimum protection:

```bash
gh api --method PUT repos/talentohq/talento-cli/branches/master/protection --input - <<'EOF'
{
  "required_status_checks": null,
  "enforce_admins": true,
  "required_pull_request_reviews": null,
  "restrictions": null,
  "allow_force_pushes": false,
  "allow_deletions": false
}
EOF
```

After 1.0.0 is out, optionally add required status checks from a green CI run. Do not enable required
commit signatures unless the org already signs every commit.

### Workflow permission elevation

```bash
gh api repos/talentohq/talento-cli/actions/permissions/workflow
```

Expected: `default_workflow_permissions` may stay `"read"`. Jobs in `release.yml` already request
`contents: write`, `id-token: write`, and `attestations: write`. If an org policy **blocks**
permission elevation, 1.0.0 cannot publish — fix that org setting before tagging.

### Apple Developer ID materials

On a Mac signed into the Developer ID team:

```bash
# After exporting Developer ID Application from Keychain Access as certificate.p12:
base64 -i certificate.p12 | tr -d '\n' > certificate.p12.b64
security find-identity -v -p codesigning
```

Copy the **Developer ID Application** identity string exactly into `APPLE_SIGNING_IDENTITY`. Delete
`certificate.p12` from disk after the secret is stored. Create an app-specific password at
appleid.apple.com for the Apple ID that belongs to the team. Store as `APPLE_APP_PASSWORD`.

```bash
gh secret set APPLE_CERTIFICATE_P12 --env stable-release-gates -R talentohq/talento-cli < certificate.p12.b64
printf '%s' "$APPLE_CERTIFICATE_PASSWORD" | gh secret set APPLE_CERTIFICATE_PASSWORD --env stable-release-gates -R talentohq/talento-cli
printf '%s' "$APPLE_ID" | gh secret set APPLE_ID --env stable-release-gates -R talentohq/talento-cli
printf '%s' "$APPLE_APP_PASSWORD" | gh secret set APPLE_APP_PASSWORD --env stable-release-gates -R talentohq/talento-cli
printf '%s' "$APPLE_TEAM_ID" | gh secret set APPLE_TEAM_ID --env stable-release-gates -R talentohq/talento-cli
gh variable set APPLE_SIGNING_IDENTITY --env stable-release-gates -R talentohq/talento-cli --body "$APPLE_SIGNING_IDENTITY"
```

Optional local smoke:

```bash
go build -o /tmp/talento ./cmd/talento
codesign --force --timestamp --options runtime --sign "$APPLE_SIGNING_IDENTITY" /tmp/talento
codesign --verify --strict --verbose=2 /tmp/talento
ditto -c -k --keepParent /tmp/talento /tmp/talento-notarize.zip
xcrun notarytool submit /tmp/talento-notarize.zip --apple-id "$APPLE_ID" --password "$APPLE_APP_PASSWORD" --team-id "$APPLE_TEAM_ID" --wait
```

Expected: `codesign --verify` succeeds and notarytool reports `Accepted`.

## 3. Key generation

The public key is **embedded in every 1.0.0 binary**. Rotating it later cannot verify old checksums.
Generate once, back up offline, never commit.

Write a throwaway program **outside** the repo (do not commit it):

```bash
cat > /tmp/talento-release-keygen.go <<'EOF'
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

func main() {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		panic(err)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	fmt.Printf("TALENTO_RELEASE_PRIVATE_KEY=%s\n", base64.StdEncoding.EncodeToString(seed))
	fmt.Printf("TALENTO_RELEASE_PUBLIC_KEY=%s\n", base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)))
}
EOF
go run /tmp/talento-release-keygen.go
rm -f /tmp/talento-release-keygen.go
```

Store both values in the team password manager before pasting into GitHub. Load them into **both**
environments:

```bash
printf '%s' "$TALENTO_RELEASE_PRIVATE_KEY" | gh secret set TALENTO_RELEASE_PRIVATE_KEY --env preview-release -R talentohq/talento-cli
printf '%s' "$TALENTO_RELEASE_PRIVATE_KEY" | gh secret set TALENTO_RELEASE_PRIVATE_KEY --env stable-release-gates -R talentohq/talento-cli
gh variable set TALENTO_RELEASE_PUBLIC_KEY --env preview-release -R talentohq/talento-cli --body "$TALENTO_RELEASE_PUBLIC_KEY"
gh variable set TALENTO_RELEASE_PUBLIC_KEY --env stable-release-gates -R talentohq/talento-cli --body "$TALENTO_RELEASE_PUBLIC_KEY"
```

Round-trip locally:

```bash
printf 'test-manifest\n' > /tmp/checksums.txt
TALENTO_RELEASE_PRIVATE_KEY=... TALENTO_RELEASE_PUBLIC_KEY=... \
  go run ./cmd/signchecksums --input /tmp/checksums.txt --output /tmp/checksums.txt.sig
```

Expected: exit 0 and a one-line base64 signature. Mismatched public/private keys must fail with
`release signing key does not match TALENTO_RELEASE_PUBLIC_KEY`.

## 4. Staging evidence

Live staging-role evidence is **not** required to publish this first stable line. The probe
contracts under `contracts/` stay in the tree for a later gate. Do not upload a placeholder report.

## 5. Release-day sequence

Stamp, signed-tag, and approve the `stable-release-gates` environment. Do not reorder.

### Enable signed tags

```bash
git config user.signingkey <key>
git config tag.gpgSign true
# or: git config gpg.format ssh
```

### Stamp and commit on the default branch (`master` today)

```bash
scripts/stamp-nix-version.sh 1.0.0
scripts/stamp-nix-version.sh --check 1.0.0
git add nix/version.nix README.md
git commit -m "release: stamp 1.0.0"
git push origin master
```

Wait for CI on that commit to go green. The README Install rewrite (stable install commands) lands in
the same stamp commit; do not publish that rewrite before release assets exist.

### Signed tag

```bash
git tag -s v1.0.0 -m "talento 1.0.0"
git push origin v1.0.0
```

Do **not** use `v1.0.0-rc.1` if the intent is a stable release. Any hyphenated prerelease suffix
routes to the preview job and skips Apple signing.

### Approve `stable-release-gates` and watch

The first stable job (`stable-preflight`) waits for environment approval. Approve it, then watch:

1. `stable-preflight` — `scripts/check-stable-gates.sh`
2. `stable-base` + `sign-macos` (amd64/arm64)
3. `stable-stage` — `repackage-signed.sh`, checksums, Ed25519, cosign, lockstep, attestations
4. `smoke-packaged-unix`
5. `stable-publish` — `gh release create v1.0.0 … --verify-tag --generate-notes` (this is a **non-prerelease**)
6. `publish-homebrew`

If `stable-preflight` fails, do not retag. Fix secrets/evidence and re-run the workflow from the
failed job, or delete the tag only if nothing was published:

```bash
git push origin :refs/tags/v1.0.0
git tag -d v1.0.0
```

Never delete the tag after `stable-publish` has created the GitHub release.

### Verify published assets

```bash
gh release view v1.0.0 -R talentohq/talento-cli
# expect: talento_1.0.0_{darwin,linux}_{amd64,arm64} archives,
# linux deb/rpm/apk, plugin zips, checksums.txt{,.sig,.sigstore.json},
# SBOMs, install.sh
```

Allowlist is `release/artifact-allowlist.json`. Extra private files must not appear.

```bash
# Sigstore (after downloading checksums.txt and checksums.txt.sigstore.json)
cosign verify-blob \
  --certificate-identity 'https://github.com/talentohq/talento-cli/.github/workflows/release.yml@refs/tags/v1.0.0' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  --bundle checksums.txt.sigstore.json checksums.txt

gh attestation verify talento_1.0.0_darwin_arm64.tar.gz -R talentohq/talento-cli
```

## 6. Post-release

### Verify each install path

```bash
# Homebrew
brew tap talentohq/tap
brew install --cask talento
talento version   # 1.0.0, source=release

# Script
TALENTO_VERSION=1.0.0 sh install.sh   # using the release asset, not a random curl of the default branch

# Nix
nix run github:talentohq/talento-cli/v1.0.0 -- version

# Go
go install github.com/talentohq/talento-cli/cmd/talento@v1.0.0
```

Expected: `talento version` reports `1.0.0` and `source` is `release` for packaged installs,
`go-install` for `go install`, `nix` for the flake.

### Edit the GitHub release notes

`release.yml` uses `--generate-notes`. After publish, replace the autogenerated body with a short
1.0.0 announcement: what the CLI is, install commands, link to `docs/distribution.md`, and the
supported platforms. Do not mention staging tenants or signing secrets.

### Stamp development version

So the default branch (`master` today) does not claim to be 1.0.0:

```bash
scripts/stamp-nix-version.sh 1.0.1-dev
git add nix/version.nix
git commit -m "chore: stamp post-release 1.0.1-dev"
git push origin master
```

Leave `internal/buildinfo.Version` at its development default unless you intentionally align it to
`1.0.1-dev` in the same commit (then run `go test ./internal/buildinfo ./internal/commands -count=1`).

## 7. Never commit

Never commit any of the following to this repository (or to the tap/bucket repos):

- Ed25519 private key material (`TALENTO_RELEASE_PRIVATE_KEY`)
- Apple `.p12` / `certificate.p12.b64` / app-specific passwords
- Windows `.pfx` / `certificate.pfx.b64` / PFX passwords
- Staging evidence JSON reports (`report.json`) if you produce one later, their base64 encodings,
  or the encoder's `$TMPDIR` upload file
- Staging access tokens, refresh tokens, customer data, or production IDs
- Fine-grained PATs used as `HOMEBREW_TAP_TOKEN`

Keep live evidence and signing materials in the password manager and in the protected
`stable-release-gates` (and `preview-release` where applicable) GitHub environment only.
