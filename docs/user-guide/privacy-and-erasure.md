# Privacy: deleting a person erases their data

Deleting a **user, customer or lead** is an erasure under GDPR Art. 17, not a recoverable soft
delete. Every personal field on the row is overwritten in place — the email address is replaced
with a random, non-routable placeholder in the reserved `.invalid` domain — and the row is only then
soft-deleted, all in a single transaction. API keys, refresh tokens and password-reset tokens
belonging to the account are purged with it. The row itself is deliberately kept so foreign keys
from tickets and tasks still resolve: business records survive, the person does not.

## Converted leads and their customers

Erasure follows the person, not the table: a lead, the customer it was converted into and that
customer's own customer-role login are erased together whichever of them you delete. Staff
accounts are never erased that way, and only an admin may delete a converted lead. Erasures made
before this rule existed are not repaired automatically: `docs/development/erasure.md` lists the queries
that find the ones with a live row left, and the supervised way to finish them; graphs whose rows
are all soft-deleted have no supported path yet.

## Deals and quotes

Deals and quotes of the erased customers and leads are kept with their links, stages, amounts,
statuses, totals and history, but the text staff typed into them can name the client, so the same
transaction overwrites it: deal titles become `[redacted]` and their notes and lost reasons are
emptied, quote titles become `[redacted]` and their notes are emptied whatever the quote status,
quote line descriptions are reset to the product names, and notifications about those deals are
deleted (#245). A client response link mailed for a quote of an erased customer stops working
(#395). Ticket and task text is kept; see `docs/development/erasure.md` for the ticket redaction
procedure.

## Consequences

Two consequences:

- **Erasure cannot be undone.** The only way to get the data back is to restore a database backup.
  To suspend access reversibly, deactivate the account instead (`is_active = false`); deactivation
  never touches personal data.
- **The email address becomes reusable**, because the original no longer exists in the table.

Tickets and tasks are unaffected — deleting one is still an ordinary soft delete.

## Legacy rows

Rows soft-deleted *before* this behaviour existed still hold personal data;
`scripts/anonymize_legacy_deleted_pii.sql` remediates them. It is manual, irreversible, and
deliberately not wired into auto-migration.

Full rationale, the cascade rules for converted leads, and the operational caveats are in
[docs/development/erasure.md](../development/erasure.md#deleting-personal-data).
