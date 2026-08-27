# Sales and CRM workflows

Keep these entities distinct: customer (company), contact (person at that company), lead (unqualified
prospect), opportunity (deal), employee (Talento user), customer stage, and opportunity stage.

## Reliable workflow

1. Resolve the customer/contact/lead/opportunity by name.
2. If several match, ask with business context; never select by guess.
3. Read the record before an update or stage transition.
4. Run the generated customer, contact, lead, opportunity, or CRM command.
5. Follow the returned write state. Lead conversion or another consequential operation may preview;
   ordinary edits may commit. The result, not the tool name, decides.

Commercial actions attach according to the live schema. Do not force a customer name into a contact
field or invent a relationship that Talento did not return.

Useful analysis includes stalled opportunities, neglected accounts, overdue next actions, and pipeline
coverage. Use returned expected/actual amounts and dates; identify incomplete or truncated reads.
