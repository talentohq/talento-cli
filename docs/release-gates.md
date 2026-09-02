# Release gates

`0.1.x` is a preview line. A `1.0.0` tag is blocked until the `stable-release-gates` GitHub
environment is approved. Live staging-role evidence is not required for this first stable line;
the probe contracts under `contracts/` remain the reviewed shape for a later gate.

- macOS binaries are Developer ID signed and notarized. Packaged Windows archives are not published.
- Packaged archives pass install, `doctor`, completion, and upgrade smoke tests on macOS and Linux.
- Packaged `talento tui` passes a real-terminal startup smoke test on macOS and Linux.
- Checksums, Ed25519 signature, Sigstore bundle, SBOMs, and GitHub attestations verify.
- The artifact allowlist contains every published asset and no private repository content or secret.

Stable assets are assembled into a private workflow artifact after macOS signing. Every packaged
archive in that artifact must pass the Linux and macOS smoke matrices before a separate job
can create the public GitHub release. Homebrew publication starts only after that GitHub
release job succeeds. Preview releases retain their prerelease-first workflow.
