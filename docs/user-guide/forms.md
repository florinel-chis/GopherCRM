# Forms

Forms are lead-capture and contact forms that the CRM defines and other websites embed. They are
under **Forms** in the sidebar. Admin, sales and support users can open them; admin and sales users
can create and edit them, and only admins can delete one.

This page covers what a submission creates and how a form that opens support tickets titles them.
The field types, the spam protection and the embed snippet are described in the
[feature inventory](../FEATURES.md) and the [API reference](../../api/index.html).

## What a submission creates

The **Submission action** section of the form builder offers one choice:

- **Create a lead.** An accepted submission adds a note to the newest live lead with the same email
  address, or creates a new lead owned by the **Lead owner** you choose.
- **Create a support ticket.** A confirmed submission opens a ticket for the live customer whose
  email address matches. Choosing it switches on **Require email confirmation before a lead or
  ticket is created**, because a ticket is opened only for a confirmed address, and the form then
  creates no lead. A submitter who matches no customer gets no ticket.
- **Create nothing.** Submissions are stored and the notification mails go out, but nothing else
  is created.

## The ticket title

A ticket opened by a form is open, medium priority and unassigned. Its description only names the
form and the submission ("Submitted through form 12, submission 345."); staff read the answers on
the submission itself.

By default the title is `Form: <form name>`, for example `Form: Contact us`. To tell the tickets of
one form apart at a glance, choose a **Ticket title field** below the submission action. It is shown
only while **Create a support ticket** is chosen, and it lists the form's text and dropdown fields
(by label and machine name) plus **None**. When a field is chosen, the ticket is titled
`<form name>: <answer>` from that field, for example `Contact us: Damaged item` for a dropdown named
`topic`. In the API the setting is the form's `ticket_title_field`, the field's machine name.

- A dropdown is the safer choice: its answers are the options you wrote. A text field puts whatever
  the visitor typed into the title.
- Line breaks, tabs and other control characters in the answer become spaces, surrounding spaces
  are removed and the whole title is cut to 255 characters.
- An empty answer keeps the default `Form: <form name>` title. So does a field that was later
  removed from the form or changed to another type; the builder refuses to save a title field that
  no longer names a text or dropdown field of the form.
- Choosing another submission action clears the setting when you save.

The title is ticket text like a title staff type in: whatever reads the ticket (the ticket list, a
webhook delivery, an automation step) sees it, and deleting the customer later does not scrub it. See
[Privacy and erasure](privacy-and-erasure.md) and the ticket text redaction procedure in
`docs/DEV_ERASURE.md`.
