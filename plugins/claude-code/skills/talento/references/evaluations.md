# Performance evaluations

An evaluation scores people on the company's competencies. When the user has already given the name, who is evaluated, who evaluates, and the competencies — ones that already exist, or new ones with a name, a scale, and a target for each job category that should be scored — create the missing competencies and then the evaluation on that turn. Pass audience, frequency, notification, and activation only when they have already chosen them.

When the request is only to set up a review, list competencies with `talento skills list-competencies`. Use `talento skills get-competency` with the exact name when you need the scale or the job-category targets. Propose which existing competencies to keep and which to add, including the scale and the target for each job category, and create them after they accept. Ask only about a choice that changes who is evaluated, who evaluates, or which competencies are scored. `talento skills list` lists the employee skill catalogue. Competencies used by evaluations are `talento skills list-competencies`.

A person is asked only the competencies that have a target for their job category. Adding a target puts that competency on every evaluation for that job category. Questions appear after a round is active, once Talento has generated them.

## Competencies

Read competency fields from `talento skills create-competency --agent --help` and the other `talento skills` help. Classify a results competency as performance and a growth competency as potential. Take job category names from `talento people list-job-categories`. After creating a competency, get it and confirm the labels and targets match what you sent: an unknown job category name is skipped. To change a target that already exists, use the ids from that get with `talento skills update-competency-tier`.

## Who is evaluated, and who evaluates

Who is evaluated, who evaluates, frequency, notification, and activation are arguments of `talento evaluations create` and `talento evaluations update`. Read the flags from `talento evaluations create --agent --help` and `talento evaluations update --agent --help`.

With no audience, only the owner is evaluated. Passing an audience replaces who is evaluated. Who evaluates is the person, their direct manager, their team manager, their office manager, or one named person who rates everyone. A team-member review is not assigned when a round starts.

Start the evaluation only when they ask, with at least one evaluator and one active competency already in place. Starting it sends no email by itself. The first opening notifies the evaluators. Later rounds email only when they asked for that notification.

After a successful create or update, report the name and the status Talento returned: who is evaluated, who evaluates, the frequency, and whether the round is open.
