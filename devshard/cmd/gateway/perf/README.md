# `perf` — what each host has been doing

Per-host history, and the one verdict derived from it that routing honours.

## What it owns

- **Samples and windows** (`sample.go`, `host.go`) — decayed success and failure counts, and two latency rings: the quantiles the escalation ladder measures a host against, and the baseline each ring is compared with.
- **Ejection** (`ejection.go`) — Envoy-style outlier detection: a host far worse than its peers is taken out of the rota, capped so ejection can never remove more than a fraction of the hosts serving a model.
- **In-flight load** (`inflight.go`) — what each host is carrying right now.
- **Capability refusals** (`capability.go`) — counts of what a host's build refused: an unsupported protocol version, a tool call it does not implement, a context length it will not take, plus the smallest context it has admitted to and the answers it wrote past the output tokens their nonce reserved.

## Boundaries

- **Capability refusals and output overruns are counted, never routed on.** They are reported so an operator knows what to fix; nothing here withholds a host from the rota over one. A version refusal in particular would retire a host for good, because a gateway serves one protocol version for its whole life.
- **Ejection is capped by a floor of available hosts**, so it cannot empty a model.
- **Every restart starts clean.** No ejections, no counts, every window at its initial value: minute-scale backoff self-heals faster than replaying stale penalties is worth ([`docs/capacity.md`](../docs/capacity.md), "Nothing here is persisted").

## When a host stops taking work

An ejection is a decision an operator has to explain afterwards, and its gauge cannot carry it: the gauge is sampled every 15 or 30 seconds while the first rung lasts 30, so the shortest withholdings pass between two scrapes. `RecordSample` narrates each edge to the journal bound by `SetNarrator`, which writes the line, and nothing in between, naming which trigger fired, the rung that set the duration, the run length, and the rate over its volume that tripped it, kept from before the reset. `HostWithheld` and `HostReturned` are called under `t.mu`, so the narrator must queue and return, as the journal does. A return has no event of its own, because an ejection lapses by the clock, so the state keeps the last edge and the first sample afterwards closes it. When that sample trips a trigger again, the lapse and the new ejection share one call, so it is narrated as a new withholding at the next rung rather than as nothing. The volume follows the host count and the rungs, never the request rate.

## How the numbers are kept

Two shapes, for two different questions.

**Outcomes are decayed counters.** Success and failure each hold a running count and the time they were last touched; reading one multiplies it by `2^(-elapsed / halfLife)`. That answers "how has this host been doing lately" without keeping a history. A consecutive-failure integer sits beside them for the trigger that does not care about rates at all. When ejection trips, the outcome counters are reset, and they are reset again on every sample that lands while the host is still ejected, so the host is judged on what it does after the ejection rather than on the evidence that caused it or on the attempts that were already in flight when it tripped. The consecutive-failure integer is not reset: a host whose first answer after release fails again is ejected at once.

**Latencies are a ring, not a decayed counter**, because the escalation ladder needs a *quantile* and a decayed counter cannot produce one. Sixty-four samples per host and model, and the p75 is refused until at least ten of them exist rather than reported from a window too short to mean anything.

## What a host is measured against

A host is judged against its own history rather than a configured number of seconds: what counts as slow depends on the model and on the hardware behind it, so a fixed threshold would be wrong for both. Each latency ring keeps a **baseline** beside its samples, the best p75 that ring has held (`host.go`, `latencyWindow.trackBaseline`). The baseline falls to a new best at once and rises a thousandth of the gap towards the current p75 on every sample, so a lucky minimum is forgotten over about a thousand answers instead of being held against the host forever. That rise is an order of magnitude slower than the ring fills, and the gap is the point: a baseline that kept up with the ring would track the latency the host currently holds, and a host degrading steadily would never read as anything but normal.

`Tracker.Pressure` is the current p75 over that baseline, for first content and for time per output token, and zero for a ring with fewer than ten samples or no baseline yet (`tracker.go`, `Tracker.Pressure`; `host.go`, `latencyWindow.pressure`). It is the delay signal the congestion windows read: past `host_congestion_slack` a host's own healthy answer narrows its window instead of widening it, which is admission backing off before anything has failed ([`limits/README.md`](../limits/README.md), "What blames which window"). Pressure withholds nothing from routing — it has no part in the ejection verdict.

## Ejection, and its two views

A host is ejected when either trigger fires: consecutive failures past the threshold, or a failure rate past its threshold once the window carries enough volume to be worth reading. The ejection lasts `base * ejectionCount`, capped at the maximum, so a repeat offender is out for longer each time. That count relaxes one rung per full healthy window since the last ejection ended, and the anchor advances with it so a long quiet stretch cannot cascade several rungs at once.

The pool-wide cap is `min(MaxEjectionFraction * hostsKnownForModel, hostsKnownForModel - MinAvailableHosts)`, floored at zero, and it is applied per model. Which hosts survive the cap is decided by ejection count, most-ejected first, with participant order breaking ties, so the cap keeps the least chronic offenders in rotation and the same set of ejections always yields the same routable set.

That cap is why there are two published views:

- **`Ejected`** is the capped verdict — whether routing actually withholds the host.
- **`Degraded`** is the verdict *before* the cap, so a host the cap had to leave in rotation is still known to be one the detector wanted out.

Both are rebuilt under the tracker's lock and published together as one atomic map — an entry carries the capped deadline and the uncapped one — so a routing decision reads a map rather than taking a lock and scanning.

State for a host and model unseen for the staleness window is evicted, swept at most once per tenth of that window rather than on every sample. `Snapshot` copies every pair's decode window in one pass under one lock — asking per host would take the lock again each time and search the map twice for a report that wants a single moment — then releases the lock before sorting each copy's samples for its p75, and reads the in-flight counts afterwards, because those live behind their own lock.

## What a capability refusal is keyed on

A tool call and a context length are properties of what the *model* asks for, so those refusals are keyed by participant **and** model. A protocol version is a property of the *build*, so that one is keyed by participant alone.

For context, the **smallest** refusal is the bound that holds: a later, larger refusal does not lift it. Each recorder reports whether the observation was new, which lets its caller narrate once instead of on every repeat, outside the lock.
