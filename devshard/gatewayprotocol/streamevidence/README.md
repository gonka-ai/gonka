# `streamevidence` — the evidence of an error stream

## What it owns

- `DataLines`: which lines of a read of SSE events are evidence. A line is kept when it starts with `data: `, after a trailing carriage return is trimmed, and is not a devshard envelope: a data line whose JSON names `"devshard_receipt"` or `"devshard_meta"` is the host's own receipt or Finish tail, not part of the executor's response, so it is dropped. Comment, event and blank lines are dropped too.
- `IsDoneLine`: whether a data line is the `[DONE]` terminator, after the `data: ` prefix and surrounding spaces are stripped. A retained stream that holds it is complete.
- `Payload`: the response payload of an error-miss claim, the JSON of `completionapi.SerializedStreamedResponse` with the evidence lines as its events. A verifier hashes these bytes and compares them with the response hash or the served hash of the Finish transaction, so both gateways must keep the same lines and serialize them the same way.
- `ProvesErrorMiss`: whether a payload proves the error a miss claim asserts. It is the check a verifier runs on the claim's response payload (`completionapi.IsTerminalErrorResponse` in `host.VerifyErrorMiss`), which an empty payload never passes. A claim whose payload fails it is rejected by every honest verifier, after a verification round that cannot succeed and in place of the timeout vote or skip the nonce would otherwise get, so both gateways attempt a claim only when it passes. It judges the payload alone: whether `sha256` of the payload matches the Finish is left to the verifier, whose reject the gateways label by how complete the retained stream was.
- The envelope keys and the terminator marker, in `vocabulary.go`.

## How the gateways use it

`devshard/cmd/devshardctl` takes `DataLines` of every streamed read of a non-probe attempt while the attempt is still speculative (`retainErrorStreamLines`) and drops them once the attempt shows no error, marks the attempt complete on `IsDoneLine`, builds the claim's payload with `Payload` (`errorMissArtifacts`), and claims only when `ProvesErrorMiss` passes on it (`errorMissOutcome`). `devshard/cmd/gateway` does the same in its error-stream retainer (`engine/miss_proof.go`): `DataLines` on each read, `IsDoneLine` for completeness, `Payload` for the proof, which exists only when `ProvesErrorMiss` passes.

## What it does not own

The retainer and how much it holds. A verifier rehashes the whole payload it is given (`devshard/host/timeout.go`, `VerifyErrorMiss`) and has no size cap tied to retention, so the bound is each gateway's own memory policy; a stream cut by it serializes to other bytes and is rejected as `hash_mismatch`. What a gateway does when its bound is hit, the memory budget it charges the retained lines to, and the release and ownership of the retained lines are its own too. Which error events a gateway reads as the terminal error of an attempt (each keeps its own classifier, and both leave capability refusals out), and where the claim's Finish comes from.

## The golden

`testdata/newlineless_final_error.sse` is a copy of the terminal-error fixture in `devshard/cmd/gateway/filters/testdata/sse/`: one error event with no final newline. `testdata/newlineless_final_error.json` is the payload `Payload` serializes from it, byte for byte, as `TestPayloadOfATerminalErrorStreamMatchesTheGolden` checks. The golden changes only together with a change to the evidence rule, which both gateways then pick up.

## The long error stream

The tests build it in code with `testutil.LongErrorStream` (`devshard/internal/testutil`), from the bound under test: 255-byte content lines until they, each counted with one separator, exceed the bound, then an error line and `[DONE]`. The table of `TestProvesErrorMissOnlyForEvidenceThatCarriesAnError` uses a bound of its own, and each gateway's test uses that gateway's bound. Each gateway's own test feeds it through its retainer and checks that it keeps exactly the leading lines that fit and marks the stream truncated. The claim tests of both gateways and the table of `TestProvesErrorMissOnlyForEvidenceThatCarriesAnError` take their verdict over the lines the retainer keeps, which carry no error, so none of them expects a claim for it.

## The error stream shapes

The `testdata/*.sse` files are error stream shapes: an error envelope with and without `[DONE]`, an error after content, a vLLM-style `"object":"error"` event, an error written `data:` without the space (`DataLines` drops it, so its payload proves nothing), and an error given as a string (not an `error` object). `TestProvesErrorMissOnlyForEvidenceThatCarriesAnError` pins the rule's verdict on each and on the long error stream, whose error lies past the bound so its verdict is that no claim is made. Each gateway has a test that streams every fixture and the long error stream through its own path and asserts it claims exactly when `ProvesErrorMiss` passes on the leading lines that fit in its own bound: `TestErrorMissClaimableFollowsTheSharedEvidenceRule` in devshardctl and `TestAnErrorStreamIsClaimedExactlyWhenTheSharedEvidenceRuleProvesIt` in `devshard/cmd/gateway/engine`.

## Imports

`common/completionapi` and the standard library.
