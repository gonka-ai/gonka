# Validation credits

Each devshard process has a credit balance per model, shared across its escrows.

- A successful, non-partial normal inference earns one credit. Cached response replay earns none.
- Each validation attempt reserves a credit before node acquisition. Dispatch spends it, including retries; failures before dispatch return it with its original expiry.
- Without credits, scheduling skips the model. Queued jobs and the lease wrapper recheck before starting work; the obligation is kept.
- Unused credits expire after **60 minutes**, a compiled setting.
- Restart resets credits to zero. Different processes have separate balances, including HA replicas.

There is no credit cap or extra concurrency limit. Existing node-manager limits
still apply. Stale recovery stops claiming when no model in the session has
credits. A deferred row does not stop the search for other credited models.
On v4, an already-acquired deferred lease remains pending until its configured
TTL expires (32 minutes by default).

Availability checks are hints; each ML attempt reserves a credit atomically.
This is a one-use credit policy, not a comparison of two rolling-hour counters.
Validation waits when generation stops and the remaining credits are spent or expire.
