# Speculative Proxy Logic

This note describes how `devshardctl` runs speculative inference in the devshard proxy.

The goal is simple:

- start with one host
- if that host looks unhealthy or too slow, add another host for the same user request
- keep doing that until one attempt wins or the proxy runs out of allowed attempts

This is the logic implemented in `devshard/cmd/devshardctl/speculative.go`.

## Why speculative execution exists

Devshard hosts can fail in several different ways:

- the host never responds
- the host sends a receipt but never produces tokens
- the host is alive but much slower than another host
- the host fails immediately at send time

Without speculation, one bad host can hold the whole user request hostage. The speculative runner reduces tail latency by allowing another host to race the original attempt.

## Core model

Each user request is represented by a set of in-flight attempts.

Each attempt has:

- a nonce
- a target host index
- send time
- optional receipt time
- optional first-token time
- final response or error

The proxy starts with one primary attempt, then may add more attempts over time.

Important detail: the proxy does not manually choose "host 2", "host 3", and so on. Instead, every call to `PrepareInference()` advances the session nonce, and the nonce naturally maps to the next host in the devshard group. That means repeated preparation automatically walks through the host set.

## High-level flow

For each request:

1. Prepare the primary inference.
2. Decide whether to start an extra attempt immediately or wait.
3. Start the primary attempt.
4. Keep watching all active attempts.
5. If no winner exists and one attempt crosses a fallback threshold, start one more attempt.
6. The first attempt that produces output becomes the winner for streaming.
7. When all attempts are resolved, process the winner and clean up failed attempts with timeout handling.

This is a progressive fanout strategy, not a broadcast-to-all-hosts strategy.

## Initial decision

Before the primary starts, the engine makes one initial decision:

- `primary_unresponsive`: if the primary host has a bad responsiveness history, start another attempt immediately
- `secondary_faster`: if the next host looks much faster from recorded performance samples, start another attempt immediately
- `receipt_timeout`: otherwise start only the primary and wait for a receipt timeout before escalating

This decision only controls the initial shape of the race. After that, the runtime escalation loop takes over.

## Escalation triggers

After the request starts, the engine keeps scanning all active attempts and asks:

"Does any current attempt justify adding one more host?"

An attempt can trigger escalation for four reasons.

### 1. Receipt timeout

If an attempt has not produced a receipt by `ReceiptTimeout`, it is treated as stuck and the proxy adds another attempt.

Use case:

- host is unreachable
- host accepts the request very late
- host stalls before confirming start

### 2. First-token timeout

If an attempt has produced a receipt but is streaming and does not produce the first token soon enough, the proxy adds another attempt.

The timeout is:

- `max(FirstTokenTimeoutCap, input_tokens * PerInputTokenFirstTokenLag)`

This scales the waiting time with prompt size while still enforcing a minimum threshold.

### 3. Non-streaming response timeout

If the request is non-streaming and the attempt does not fully finish soon enough after send time, the proxy adds another attempt.

The timeout is:

- `max(NonStreamResponseFloor, input_tokens * PerInputTokenResponseLag)`

This is the non-streaming equivalent of the first-token fallback.

### 4. Immediate attempt failure

If an attempt finishes with an error before succeeding, the proxy does not wait for the old timeout window. It can immediately escalate to the next host.

This is what makes the "first host dead, second host dead, third host wins" case work.

## What changed in the universal version

The earlier version only handled:

- primary
- optional one secondary

That meant:

- if the primary was dead, the proxy could try one more host
- if that second host was also dead or also too slow, the request still failed

The current version treats the request as a list of attempts instead of a fixed pair.

So now:

- a slow primary can trigger a secondary
- a slow secondary can trigger a tertiary
- a dead primary can trigger a secondary immediately
- a dead secondary can trigger a tertiary immediately

This makes the fallback logic universal across the whole devshard group, subject to the configured attempt limit.

## Winner selection

For streaming requests, the winner is the first attempt that produces output chunks. Once a winner is selected:

- only the winner's stream is forwarded to the client
- loser streams are suppressed
- the proxy may still keep waiting briefly for the other attempts to finish so it can process or time them out cleanly

For non-streaming requests, a finished successful attempt is also treated as the winner even if no stream chunk was observed.

## Attempt limit

The runner does not have to use every host forever.

`MaxSpeculativeAttempts` controls the upper bound:

- `0` means "allow up to the full group size"
- any positive number caps the total attempts for one user request

This is important because every extra attempt is real devshard work and may later require timeout cleanup.

## Cleanup and timeout handling

When one attempt succeeds and others fail, the request still succeeds for the user. The losers are cleaned up separately:

- finished successful attempts are processed into session state
- failed attempts go through timeout vote collection and timeout diff submission

If no attempt succeeds, the whole request fails.

## Answer to the "slow first token" question

Yes: the new logic changes that path too.

Previously:

- primary is slow to first token
- proxy starts one secondary
- if the secondary is also slow, the logic stops there

Now:

- primary is slow to first token
- proxy starts a secondary
- if the secondary is also slow to first token, it can trigger a tertiary
- this continues until some attempt wins or the attempt limit is reached

The same principle applies to:

- slow receipt
- slow non-streaming completion
- immediate host failure

## Regression coverage

The codebase includes a regression test for the multi-dead-host case:

- `TestRunInference_SpeculativeFallsThroughMultipleDeadHosts`

That test kills the first two routed hosts and verifies that the third attempt wins.
