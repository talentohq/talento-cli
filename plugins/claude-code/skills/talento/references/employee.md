# Employee workflows

Start with `talento commands --available --agent`; employee profiles normally receive a self-service
subset, and the live result decides the boundary.

## Working day

- Inspect hours with `talento time ...`; use Talento's total, contract, and extra-hour figures.
- Starting/stopping an activity may commit immediately. Do not add a confirmation unless the result
  is a preview.
- Broad team or office requests may be unavailable or reduced to the employee's own data. State the
  applied scope shown by Talento.

## Time off and expenses

- Resolve absence or expense categories by name before creating a request.
- Absences and expenses can be persisted requests whose approval remains pending. Report both facts:
  the request was filed and its returned approval state.
- Preserve separate dates and partial-day hours exactly; do not collapse non-contiguous dates into a
  range.

## Tasks, todos, goals, and learning

- Project tasks (`talento tasks …`) belong to a project. They are not goal checklists.
- Goal actions (`talento goals list-actions|create-action|update-action|delete-action`) are
  checklist items on a goal. Use them when the user wants to break a goal into simple todos.
- Personal todos (`talento todos …`) are private to the current user. Managers never see them.
- Do not call `talento tasks create-task` with a goal name; the gateway rejects that and
  tells you to use `create_goal_action`.
- Use names and the returned context to update the intended record; resolve ambiguity first.
- Treat training visibility and authoring commands as server-authoritative. An employee may be able
  to discover or take training without being allowed to author it.

For any write, follow the state rules in [core.md](core.md).
