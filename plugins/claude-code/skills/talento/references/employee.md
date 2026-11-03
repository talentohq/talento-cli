# Employee workflows

Start with `talento commands --available --agent`; employee profiles normally receive a self-service
subset, and the live result decides the boundary.

## Working day

- Inspect hours with `talento time ...`; use Talento's total, contract, and extra-hour figures.
- Device details and unusual-activity warnings appear only when Talento returns them. An employee
  read does not include them. Do not invent a device or a warning.
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
- Omitting the end date files a single day. An open absence uses `--open-ended` only when the
  category listing says the return date can be blank. Do not combine that with a list of separate dates.

## Tasks, todos, goals, and learning

- Project tasks (`talento tasks …`) belong to a project. They are not goal checklists.
- Goal actions (`talento goals list-actions|create-action|update-action|delete-action`) are
  checklist items on a goal. Use them when the user wants to break a goal into simple todos.
- Personal todos (`talento todos …`) are private to the current user. Managers never see them.
- Do not call `talento tasks create-task` with a goal name; the gateway rejects that and
  tells you to use `create_goal_action`.
- `talento goals list` defaults to goals you are responsible for. A goal you created for someone
  else is not in that list. Use `--scope all`, or `--employee-name` for the person responsible.
- `talento goals create` and `talento goals update` save immediately. You are the responsible
  person unless you pass `--responsible-name`. You can name yourself or someone you manage for
  goals. Prefer the name over `--responsible-id`.
- `talento visibility manage` reads or changes who can see a goal or a project. Pass
  `--record-type goal` or `--record-type project` and `--record-name`. Leave off every add and
  remove flag to read the audience. Any add or remove flag changes that audience and leaves the
  other rows. It saves immediately. You can change it only when Talento lets you modify that record.
- A task is seen by whoever can see its project. Pass the project.
- The person responsible for a goal always stays in the audience. An unknown or ambiguous name
  saves nothing.
- Add offices, teams, or people by name with `--add-offices-item`, `--add-teams-item`, and
  `--add-employees-item`. Remove them the same way. `--add-roles-item` takes `all_reportees`,
  `direct_reportees`, `team_reportees`, `office_reportees`, `team_colleagues`, `office_colleagues`,
  or `company_colleagues`. `--add-company` shares the record with the whole company, and only when
  the user asked for everyone.
- Change a survey audience with `talento surveys update`, and an evaluation audience with
  `talento evaluations update`. Those commands replace the whole audience. Sales invoices stay on
  the invoice commands.
- Use names and the returned context to update the intended record; resolve ambiguity first.
- Treat training visibility and authoring commands as server-authoritative. An employee may be able
  to discover or take training without being allowed to author it.

For any write, follow the state rules in [core.md](core.md).
