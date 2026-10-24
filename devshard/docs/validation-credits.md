# Validation credits

Each devshard binary has a credit balance per model, shared across its escrows.

- A successful, non-partial normal inference earns one credit. Replaying a stored response earns none.
- Each validation attempt reserves a credit before node acquisition. Dispatch spends it, including retries; failures before dispatch return it with its original expiry.
- Without credits, scheduling skips the model. Queued jobs and the lease wrapper recheck before starting work; the obligation is kept.
- Unused credits expire after **60 minutes**, a compiled setting.
- Restart resets credits to zero. Different binaries have separate balances.

There is no credit cap or extra concurrency limit. Existing node-manager limits
still apply. Stale recovery stops claiming when no model in the session has
credits. Claims are model-agnostic, so a mixed-model session can still claim and
release exhausted models while looking for credited work. A deferred row does
not stop that search. Availability checks do not reserve credits; each ML attempt
still reserves atomically before node acquisition.
