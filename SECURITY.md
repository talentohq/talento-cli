# Security policy

Please report vulnerabilities privately through GitHub Security Advisories for
`talentohq/talento-cli`. Do not open a public issue containing tokens, authorization codes,
customer data, account names, preview IDs, or exploit details.

Supported security fixes target the latest preview and stable release lines. Reports should include
the CLI version, operating system, installation method, and a minimal reproduction with every
credential and customer value redacted.

The CLI intentionally uses only `https://mcp.talentohq.com/mcp`, validates OAuth discovery and PKCE,
stores secrets in the system credential store by default, and has no telemetry. Release archives are
covered by exact-workflow-identity Sigstore verification of SHA-256 checksums and build provenance;
stable Windows direct installs also pin the Authenticode publisher. See
[docs/distribution.md](docs/distribution.md) for verification instructions.
