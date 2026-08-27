---
name: talento
description: Use TalentoHQ through the local talento CLI for HR, time, work, talent, CRM, finance, reports, and public-page workflows. Trigger when the user mentions Talento, TalentoHQ, company people or operations that should be read or changed in Talento, or asks what their Talento profile can do.
---

# TalentoHQ

Use the local `talento` executable. Do not create a second MCP connection or ask for a company URL.
The selected CLI profile owns the OAuth grant and Talento remains authoritative for tenant, module,
role, permission, visibility, calculations, and write behavior.

## Start with live discovery

1. Run `talento --agent --help` when you need the top-level contract.
2. Run `talento commands --available --agent` before planning an unfamiliar workflow.
3. Drill into `talento <domain> --agent --help` and then the chosen subcommand's help.
4. If the executable is missing or authentication is required, say exactly that. Never invent a
   fallback URL, token, command, or capability.

Do not rely on a memorized command list: the CLI is generated from a reviewed schema snapshot and
the live profile may expose a smaller role- and permission-scoped set.

## Operating rules

Read [references/core.md](references/core.md) before any write, multi-step lookup, or analysis.
Then load only the relevant workflow reference:

- [references/employee.md](references/employee.md) — self-service time, absences, expenses, tasks,
  todos, goals, and training.
- [references/manager-hr.md](references/manager-hr.md) — people, schedules, approvals, skills,
  evaluations, recruitment, onboarding, and training administration.
- [references/sales.md](references/sales.md) — customers, contacts, leads, opportunities, and CRM.
- [references/finance.md](references/finance.md) — invoices, providers, purchase documents, and items.
- [references/external.md](references/external.md) — one-company-at-a-time external-user boundaries.
- [references/custom-views.md](references/custom-views.md) — public-page editing and preview workflow.

## Communication

- Answer in the user's language; CLI commands, flags, diagnostics, and JSON keys stay in English.
- Lead with the result or insight, then the supporting detail.
- Refer to records by name, date, and business context. Keep internal IDs out of prose.
- Preserve localized values returned by Talento; do not translate a status into a different state.
- Never claim that a preview committed, an error succeeded, an approval-pending request was approved,
  or a missing command can be bypassed.
