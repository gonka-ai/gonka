# `streamevidence` — the evidence of an error stream

## What it owns

- `DataLines`: which lines of a read of SSE events are evidence. A line is kept when it starts with `data: `, after a trailing carriage return is trimmed, and is not a devshard envelope: a data line whose JSON object has a top-level `devshard_receipt` or `devshard_meta` key is the host's own receipt or Finish tail, not part of the executor's response, so it is dropped. Comment, event and blank lines are dropped too.
- `IsDoneLine`: whether a data line is the `[DONE]` terminator, after the `data: ` prefix and surrounding spaces are stripped. A retained stream that holds it is complete.
- `Payload`: the response payload of an error-miss claim, the JSON of `completionapi.SerializedStreamedResponse` with the evidence lines as its events. A verifier hashes these bytes and compares them with the response hash or the served hash of the Finish transaction, so the lines kept and their serialization must not change.
- `ProvesErrorMiss`: whether a payload proves the error a miss claim asserts. It is the check a verifier runs on the claim's response payload (`completionapi.IsTerminalErrorResponse` in `host.VerifyErrorMiss`), which an empty payload never passes. A claim whose payload fails it is rejected by every honest verifier, after a verification round that cannot succeed and in place of the timeout vote or skip the nonce would otherwise get, so the gateway attempts a claim only when it passes. It judges the payload alone: whether `sha256` of the payload matches the Finish is left to the verifier, whose reject the gateway labels by how complete the retained stream was.
- The envelope keys and the terminator marker, in `vocabulary.go`.

## How the gateway uses it

`devshard/cmd/gateway` uses it in its error-stream retainer (`engine/miss_proof.go`): `DataLines` on each read, `IsDoneLine` for completeness, `Payload` for the proof, which exists only when `ProvesErrorMiss` passes.

## What it does not own

The retainer and how much it holds. A verifier rehashes the whole payload it is given (`devshard/host/timeout.go`, `VerifyErrorMiss`) and has no size cap tied to retention, so the bound is the gateway's own memory policy; a stream cut by it serializes to other bytes and is rejected as `hash_mismatch`. What the gateway does when its bound is hit, the memory budget it charges the retained lines to, and the release and ownership of the retained lines are its own too. Which error events the gateway reads as the terminal error of an attempt (its own classifier, which leaves capability refusals out), and where the claim's Finish comes from.

## The golden

`testdata/newlineless_final_error.sse` is a copy of the terminal-error fixture in `devshard/cmd/gateway/filters/testdata/sse/`: one error event with no final newline. `testdata/newlineless_final_error.json` is the payload `Payload` serializes from it, byte for byte, as `TestPayloadOfATerminalErrorStreamMatchesTheGolden` checks. The golden changes only together with a change to the evidence rule, which the gateway then picks up.

## The long error stream

The tests build it in code with `testutil.LongErrorStream` (`devshard/internal/testutil`), from the bound under test: 255-byte content lines until they, each counted with one separator, exceed the bound, then an error line and `[DONE]`. The table of `TestProvesErrorMissOnlyForEvidenceThatCarriesAnError` uses a bound of its own, and the gateway's test uses the gateway's bound. That test feeds it through the retainer and checks that it keeps exactly the leading lines that fit and marks the stream truncated. The gateway's claim tests and the table of `TestProvesErrorMissOnlyForEvidenceThatCarriesAnError` take their verdict over the lines the retainer keeps, which carry no error, so none of them expects a claim for it.

## The error stream shapes

The `testdata/*.sse` files are error stream shapes: an error envelope with and without `[DONE]`, an error after content, a vLLM-style `"object":"error"` event, an error written `data:` without the space (`DataLines` drops it, so its payload proves nothing), and an error given as a string (not an `error` object). `TestProvesErrorMissOnlyForEvidenceThatCarriesAnError` pins the rule's verdict on each and on the long error stream, whose error lies past the bound so its verdict is that no claim is made. `TestAnErrorStreamIsClaimedExactlyWhenTheSharedEvidenceRuleProvesIt` in `devshard/cmd/gateway/engine` streams every fixture and the long error stream through the gateway and asserts it claims exactly when `ProvesErrorMiss` passes on the leading lines that fit in its bound.

## Imports

`common/completionapi` and the standard library.
