# External-user workflows

An external user can work with more than one company over time, but one Talento CLI profile is one
company grant and one tenant at a time.

- Select the intended named profile before any read or write. Echo the profile/company context when a
  mistake would be costly.
- Never carry an ID, preview ID, cached result, or assumption from one profile into another.
- Expect a narrower live command catalogue than an employee, manager, or admin. A missing tool is a
  server-enforced capability boundary, not a prompt to retry through raw `tools call`.
- When reporting across companies, run separate profile-scoped reads, label each source, disclose
  differing scope/permissions, and never present the combination as a Talento server-side total.

If the requested company has no configured profile, ask the user to authenticate a separate profile;
do not ask for a tenant URL or token.
