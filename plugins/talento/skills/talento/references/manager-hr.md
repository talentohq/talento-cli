# Manager and HR workflows

Managers and HR/admin users can see different people and operations. Never assume that a manager has
admin-wide access; inspect the live catalogue and the applied scope.

## People and schedules

- Resolve employees by name and verify the returned employee before acting on another person.
- Scheduling, swaps, assignments, and bulk clock-in generation often return previews. The live state
  decides; do not memorize a universal confirmation rule.
- A successful absence, reschedule, booking, or other request may still be pending approval. Preserve
  its lifecycle state.
- The schedule catalog and a person's assignment are different writes. List the catalog before naming
  a category, weekly pattern, or rotating shift.
- `talento schedules category-create`, `category-update`, `pattern-create`, `pattern-update`,
  `set-hours`, `shift-create`, and `shift-update` change that catalog. They preview and wait for
  confirmation. Weekdays on `set-hours` are 0 Sunday through 6 Saturday. Other weekdays stay as they are.
- `talento schedules assign` names a category or a rotating shift. A shift with one pattern can omit
  the category. `assignment-update` and `assignment-remove` need the person and the date the
  assignment applies. Omitting the end date on an assignment replaces the current schedule from the
  start date; including it is a temporary override.
- An ordinary reschedule uses a date range plus a category, shift, or colleague. An on-call request
  uses `request_kind=on_call`, `on_call_date`, and `on_call_mode` (`change`, `cover`, or `swap`).
  Cover names who takes the shift. Swap names the colleague and their on-call date.
- Managers approve or reject a pending request with `talento schedules reschedule-decide`. Passing
  `approved` on create, update, or manage applies the change after confirmation. An on-call change
  with no replacement has to be completed before it can be approved.
- Manager and admin clock-in reads include the entry and exit device and review warnings when Talento
  recorded them. If a line says the device was not recorded, leave it unknown. Employee reads do not
  include those lines.

## Talent

- For skills and competency work, inspect the existing framework and job categories before changing
  targets or scales.
- Writing a survey, choosing who receives it, and turning it on are in [surveys.md](surveys.md).
- Writing a performance evaluation, choosing who is evaluated and who evaluates, and turning it on are in [evaluations.md](evaluations.md).
- Evaluation and survey results may be incomplete when responses are missing. Say what population the
  result covers.
- Recruitment steps can trigger candidate communication. If Talento previews a move or a new
  candidate placed on a notifying step, present that consequence and confirm only after the user agrees.
- Adding a candidate by hand places them on a pipeline step of a job offer. Name and email are
  required. The first step is used when no step is named.
- Candidate skills are catalog skills recorded on that person, with an obtained date and optional
  notes. Add, update, and remove them by skill name. A detailed candidate listing shows each entry.
- Goal actions are checklists on a goal, not project tasks. List them before adding a duplicate.
- Training authoring has draft, review, requested-changes, published, and archived lifecycle states.
  Use the available lifecycle command and report the returned state; do not claim publication from a
  review submission. Returning a published course to draft is a lifecycle write, not a delete.
- Training skill links accept a proficiency (`basic`, `intermediate`, `advanced`, `expert`) via
  `--skills` JSON. `--skill-ids` still works but new links have no level; prefer `--skills`.
- A course with `--external` is hosted on another platform and needs `--external-url`. Those
  courses do not take in-app topics.
- Meeting question templates are reusable 1:1 and hiring-interview prompts, not surveys. List them
  before creating a duplicate set. Deleting a template fails while it still has questions.
- Onboarding actions that require approval are not complete merely because an update request persisted.

## Useful management reads

Surface pending approvals, unstaffed or conflicting schedules, goals at risk, missing evaluation
responses, recruiting bottlenecks, and onboarding actions waiting on someone. Support the insight with
Talento's returned counts and scope rather than reconstructing totals.
