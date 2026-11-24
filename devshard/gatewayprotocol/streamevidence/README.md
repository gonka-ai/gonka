# `streamevidence` — the evidence of an error stream

## What it owns

- `DataLines`: which lines of a read of SSE events are evidence. A line is kept when it starts with `data: `, after a trailing carriage return is trimmed, and is not a devshard envelope: a data line whose JSON names `"devshard_receipt"` or `"devshard_meta"` is the host's own receipt or Finish tail, not part of the executor's response, so it is dropped. Comment, event and blank lines are dropped too.
- `IsDoneLine`: whether a data line is the `[DONE]` terminator, after the `data: ` prefix and surrounding spaces are stripped. A retained stream that holds it is complete.
- `Payload`: the response payload of an error-miss claim, the JSON of `completionapi.SerializedStreamedResponse` with the evidence lines as its events. A verifier hashes these bytes and compares them with the response hash or the served hash of the Finish transaction, so both gateways must keep the same lines and serialize them the same way.
- `ProvesErrorMiss`: whether a payload proves the error a miss claim asserts. It is the check a verifier runs on the claim's response payload (`completionapi.IsTerminalErrorResponse` in `host.VerifyErrorMiss`), which an empty payload never passes. A claim whose payload fails it is rejected by every honest verifier, after a verification round that cannot succeed and in place of the timeout vote or skip the nonce would otherwise get, so both gateways attempt a claim only when it passes. It judges the payload alone: whether `sha256` of the payload matches the Finish is left to the verifier, whose reject the gateways label by how complete the retained stream was.
- `MaxRetainedBytes`: the most evidence a gateway retains of one attempt. See "The retention bound".
- The envelope keys and the terminator marker, in `vocabulary.go`.

## How the gateways use it

`devshard/cmd/devshardctl` takes `DataLines` of every streamed read of a non-probe attempt while the attempt is still speculative (`retainErrorStreamLines`) and drops them once the attempt shows no error, marks the attempt complete on `IsDoneLine`, builds the claim's payload with `Payload` (`errorMissArtifacts`), and claims only when `ProvesErrorMiss` passes on it (`errorMissOutcome`). `devshard/cmd/gateway` does the same in its error-stream retainer (`engine/miss_proof.go`): `DataLines` on each read, `IsDoneLine` for completeness, `Payload` for the proof, which exists only when `ProvesErrorMiss` passes. Both bound their retainer with `MaxRetainedBytes`.

## The retention bound

`MaxRetainedBytes` is 1 MiB. A retainer counts each kept line as its bytes plus one separator. It stops at the first line that does not fit and marks the stream truncated.

A verifier needs the whole stream. It hashes the payload and compares the hash with the response hash or the served hash in the executor's signed Finish, and the executor hashed its whole body (`devshard/host/timeout.go`, `VerifyErrorMiss`). A truncated prefix serializes to other bytes. Every verifier rejects it as `hash_mismatch`, the claim gathers no votes, and the executor is paid for its error. So the bound decides which error streams can be claimed at all. Two gateways with different bounds pay the same host differently for the same stream, which is why the bound is a protocol value and lives here. No verifier-side limit sits near it: the verify request is read up to 10 MiB.

The bound is 1 MiB rather than larger because it is paid on the success path too. Both gateways retain the lines of every non-probe streamed attempt from its first read until the attempt shows no error, so every long successful stream holds up to the bound while it streams. `devshard/cmd/gateway` charges retention to no budget, so its worst case is the bound times the attempts in flight. devshardctl charges it to its participant and global classify budgets, which it shares with classify reassembly, so about ten long successful streams in flight to one participant fill its 10 MiB budget and about a hundred in all fill its 100 MiB global budget. The cost of 1 MiB is that an error following more than 1 MiB of streamed data lines cannot be claimed by either gateway.

Each gateway may stop below the bound when its own memory budget refuses: devshardctl when its participant or global classify budget is full, `devshard/cmd/gateway` when its reassembly budget drops bytes. Without that pressure both keep the same prefix of the same stream. When the retained prefix holds no error line, `ProvesErrorMiss` fails on it and neither gateway posts a claim.

## What it does not own

The retainer: what a gateway does when the bound is hit, and the memory budget it charges the retained lines to. Release and ownership of the retained lines. Which error events a gateway reads as the terminal error of an attempt (each keeps its own classifier, and both leave capability refusals out), and where the claim's Finish comes from.

## The golden

`testdata/newlineless_final_error.sse` is a copy of the terminal-error fixture in `devshard/cmd/gateway/filters/testdata/sse/`: one error event with no final newline. `testdata/newlineless_final_error.json` is the payload `Payload` serializes from it, byte for byte, as `TestPayloadOfATerminalErrorStreamMatchesTheGolden` checks. The golden changes only together with a change to the evidence rule, which both gateways then pick up.

## The long error stream

`testdata/long_error_stream.sse` is 4200 content lines of 255 bytes each, about 1 MiB, then an error line and `[DONE]`. Counted with their separators the first 4096 lines fill `MaxRetainedBytes` exactly, so a retainer that counts without the separator, or stops at a full bound rather than past it, keeps a different number. Each gateway's own test feeds it through its retainer and checks that it keeps exactly those 4096 lines and marks the stream truncated. The claim tests of both gateways and the table of `TestProvesErrorMissOnlyForEvidenceThatCarriesAnError` take their verdict over the lines the retainer keeps, which carry no error, so none of them expects a claim for it.

## The error stream shapes

The other `testdata/*.sse` files are error stream shapes: an error envelope with and without `[DONE]`, an error after content, a vLLM-style `"object":"error"` event, an error written `data:` without the space (`DataLines` drops it, so its payload proves nothing), and an error given as a string (not an `error` object). `TestProvesErrorMissOnlyForEvidenceThatCarriesAnError` pins the rule's verdict on each and on the long error stream, whose error lies past the bound so its verdict is that no claim is made. Each gateway has a test that streams every fixture through its own path and asserts it claims exactly when `ProvesErrorMiss` passes on the leading lines that fit in `MaxRetainedBytes`: `TestErrorMissClaimableFollowsTheSharedEvidenceRule` in devshardctl and `TestAnErrorStreamIsClaimedExactlyWhenTheSharedEvidenceRuleProvesIt` in `devshard/cmd/gateway/engine`.

## Imports

`common/completionapi` and the standard library.
