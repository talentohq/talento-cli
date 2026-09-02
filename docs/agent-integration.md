# Agent integration

`skills/talento/` is canonical and follows the Agent Skills specification. The top-level skill is
small; employee, manager/HR, sales, finance, external-user, custom-view, and core write guidance load
progressively.

On a terminal, `talento setup` is a line-oriented wizard. It selects or creates a named profile,
authenticates it with the existing OAuth/PKCE flow, verifies the local grant, detects and selects one
or more supported agents, installs managed files at user or project scope, optionally prints shell
completion guidance, and finishes with the normal doctor health checks. A canceled or failed run
keeps completed profile, grant, and managed-file stages and prints an idempotent resume command.

In non-interactive contexts an explicit `--agent` is required unless `--yes` authorizes all detected
agents. This automation path installs managed files only; it never prompts, creates a profile,
mutates credentials, or opens a browser. Compatible clients receive the shared skill at
`~/.agents/skills/talento`; dedicated client paths receive generated copies or lightweight wrappers.
Removing an adapter keeps the canonical skill while another installed wrapper still references it
and removes it with the last dependent wrapper. Supported IDs are `claude-code`, `codex`, `gemini`,
`grok`, `copilot`, `cursor`, `windsurf`, and `opencode`.

Each supported client is represented by a capability adapter with explicit detection, install,
remove, diagnosis, version-probe, and scope support. All current mutations are managed-file-backed;
the CLI does not guess vendor plugin commands. In particular, Codex remains file-backed because the
[official OpenAI plugin documentation](https://developers.openai.com/plugins/deploy/connect-chatgpt)
describes packaging and product connection, but does not establish a stable local Codex CLI
plugin-install command. Claude Code is also file-backed until a
Talento marketplace source and its registration contract are published. Executable `--version`
probes are time-bounded and run with an isolated disposable home, config, temp directory, and no
ambient secret variables; they never touch the real agent state, start a session, or send a prompt.

Use `talento skill status` (optionally filtered with `--integration`) to inspect executable detection and
version, supported method/scopes, tracked installation state, expected and installed integration
versions, and repair commands. Add `--verbose` for managed paths and expected/actual digests. The same concise per-adapter checks appear in
`talento doctor`; `--verbose` adds safe paths and integrity details. Upgrading the CLI marks an older
managed integration stale and suggests `talento skill update` rather than silently rewriting it.
Managed Codex and Claude Code skill directories include `.talento-integration.json`, stamped from
the running CLI build version so development and release provenance remain honest.

The CLI tracks only files it creates. Reinstalling is idempotent. Modified files are preserved unless
`--force` creates a timestamped backup. Removing a skill does not log out profiles, and logging out a
profile does not remove skills.

Local shell-capable agents invoke `talento --agent` and share the selected CLI profile. Hosted agents
cannot use local files or credentials; `talento handoff` prints the fixed generic MCP endpoint and
requires the hosted product to complete its own OAuth flow. Marketplace or administrator approval
may be required.
