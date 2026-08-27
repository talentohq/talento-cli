# Custom public views

Custom views edit a named draft version of a public page. They do not grant access to arbitrary
Talento data, scripts, remote assets, or publication controls.

## Workflow

1. List views, then versions.
2. Create a private version when the user wants a safe branch.
3. Read the latest master before every edit sequence.
4. Use a targeted edit for a small change and a full write only for a true redesign.
5. Preview normal and sparse data variants.
6. Give the preview location returned by Talento and state that a human publishes/activates in the web.

Copy edit anchors exactly from the latest returned master. Never invent tokens. Respect the server's
sandbox errors: adjust a fixable validation error, but do not retry a policy limit such as scripts,
iframes, external URLs, or CSS `url()`.

These writes can commit draft content directly without `confirm_action`. Follow the returned state like
every other command; do not invent a preview-confirm step and do not imply that the public page changed.
