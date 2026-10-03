# Finance workflows

Keep the two sides separate:

- Sales: customer -> sales invoice.
- Purchasing: provider -> purchase order and/or purchase invoice.
- Catalogue items are reusable records, not document lines or providers.

## Documents and money

- Resolve the customer/provider and inspect existing drafts before creating another document.
- `talento invoices list` includes draft, sent, rectified, and cancelled invoices unless `--status`
  names one of those.
- A won opportunity becomes a draft sales invoice through `talento opportunities convert-to-invoice`.
  Use that command when the deal should be invoiced.
- Pass line data exactly through the command schema. Do not calculate tax, discounts, withholdings,
  totals, or legal status yourself; use Talento's preview/result.
- Invoice sending is available only if the live command exists. Because it communicates outside the
  company, carefully present any preview and confirm only with explicit authorization.
- Purchase-order transitions and purchase-invoice updates must preserve the lifecycle returned by
  Talento. A submission for approval is not approval; a recorded paid date is not a bank transfer.
- If tax identifiers, legal requirements, module access, or permissions block the operation, report
  the server error and the required human next step. Do not weaken the request or substitute another
  company record.

For receivables/payables analysis, use server totals and due dates and disclose truncation. Never infer
cash movement from a document state alone.
