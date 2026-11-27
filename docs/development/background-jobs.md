# Background runtimes

> **For:** Developers changing work that outlives a request. Part of the [documentation index](../README.md).

The work that outlives a request: the AEO runtime and its shutdown, the automation runtime,
outbound webhooks, the form retention purge, form mail delivery, form attribution, form ticket
intake, and the AEO runtime validation. The shared building blocks are summarised under
[Background runtimes and outbound requests](./README.md#background-runtimes-and-outbound-requests).
Part of the developer guide; the index is in [development/README.md](./README.md).

The topics have one page each:

- [AEO runtime](aeo-runtime.md): the AEO background work, its shutdown, and the runtime validation and candidate inspection.
- [Automation runtime](automation-runtime.md): the automation runtime.
- [Outbound webhooks](outbound-webhooks.md): outbound webhook delivery (#345).
- [Form background work](form-background-work.md): the form retention purge, form mail delivery (#216), form attribution and the form-submitted event, and form ticket intake (#213).
