# Gateway protocol library — design

**Status:** Implemented in `devshard/gatewayprotocol` for settlement (`settlement`, `settlementwire`), the timeout claim (`timeoutclaim`), the probe budget (`probe`) and the evidence of an error stream (`streamevidence`).

**Audience:** Engineers changing what the gateway, `devshard/cmd/gateway`, sends to hosts, verifiers or the chain.

This document says what belongs in the shared tree, where it lives and how its boundary is enforced. The package READMEs under `devshard/gatewayprotocol` say what each package owns.

## 1. What is protocol

Code is protocol when a host, a verifier or the chain would reject, pay differently or judge differently if it received a result built another way. Everything else is gateway-local: locking, memory ownership, panic recovery, HTTP, logging, metrics, and also choices no other party can tell apart, such as a byte layout the chain decodes to the same message, a prompt literal, or how the gateway reacts to an error.

Two consequences follow.

- The settle wire layout is protocol only where the chain judges it. A zero-valued field written or omitted decodes to the same message, so `devshard/cmd/gateway` marshals the chain's generated type, which omits zeros. Fields 7 and 8 of a host-stats entry are protocol: a chain whose host-stats message defines `validated = 7` and `finished = 8` rebuilds the settlement root from them, and the generated type stops at field 6. The chain module in this tree hashes fields 1 to 6. Their encoding exists twice, in `common/chain/tx` and in `settlementwire`, because `gatewayprotocol` may not import runtime code. `settlementwire` is still shared because it owns the mapping from the settle message onto `MsgSettleDevshardEscrow`.
- Content that is already decided in a shared package gets no package here. The timeout reason a host sees is chosen in `devshard/user`, and the state-root mismatch errors are defined in `devshard/types` and `devshard/state`. The probe's token floor is `state.MinReservationTokens`, enforced by the host's state machine; what lives here is only how a probe applies it, in `probe`.

## 2. Module location

One package tree inside the `devshard` module. A separate module does not pay: the tree needs `devshard/state`, `devshard/types`, `devshard/host` and `devshard/user`, and the gateway needs the tree, so a second `go.mod` would form a `replace` cycle. `common` is a separate module that does not require `devshard`, so nothing in `common` can import the tree. Inside `devshard`, the shared packages the tree imports import nothing under `devshard/cmd`, and the guard keeps them from importing the tree.

```
devshard/gatewayprotocol/       README.md, guard_test.go
  probe/                        README.md, budget.go, budget_test.go
  settlement/                   README.md, message.go, order.go, root.go, tests
  settlementwire/               README.md, marshal.go, marshal_test.go, testdata/ (settle goldens)
  streamevidence/               README.md, lines.go, payload.go, vocabulary.go, tests, testdata/ (error stream golden and shapes, long error stream fixture)
  timeoutclaim/                 README.md, claim.go, payload.go, tests
```

## 3. Packages

### 3.1 `settlement`

The settle message, the slot order of its lists, the signatures a settle posts and the settlement root; the package README gives each rule and its reason. `Root` hashes the host stats exactly as given and never filters, so the gateway gets the root of the map it passes, and `PresentStats` drops nil entries.

It does not own escrow-id parsing, signature collection, narration of a settlement the chain would refuse. The gateway keeps those.

### 3.2 `settlementwire`

Owns `Marshal(settler, message)`: the mapping from `settlement.Message` onto `MsgSettleDevshardEscrow` and its marshalling through the chain's generated type, plus host-stats fields 7 and 8, which that type lacks. It does not own the transaction envelope, signing or broadcasting, which stay in `devshard/cmd/gateway/chain` and `common/chain/tx`, nor the create-escrow message. It is the only package in the tree allowed to import chain types.

### 3.3 `timeoutclaim`

The choice between an error-miss claim and a timeout vote for a nonce left unfinished (`ClaimsMiss`), the Finish a claim carries (`FinishTx`) and the timeout vote's payload (`Payload`): one rule each.

It does not own whether the claim's payload proves the error, which is `streamevidence.ProvesErrorMiss` (§3.5). Deadlines and waiting stay in the gateway.

### 3.4 `probe`

The token budget of a probe: `MaxTokens` and `Params`. `devshard/cmd/gateway`'s warm-up probe and its ghost burn use it. The probe prompt is not owned: a host accepts any prompt that parses and asks for no more than `MaxTokens`, so the gateway keeps its own literal and formats `MaxTokens` into it.

### 3.5 `streamevidence`

Which lines of an error stream are evidence, the `[DONE]` test, the payload they serialize into and whether the payload proves the error. A verifier hashes the payload and compares it with the Finish transaction's hashes, so a line kept or dropped differently turns an accepted claim into a rejected one.

It does not own the retainer or how much it holds: the gateway sets its own bound (the package README says why), decides what happens when it is hit and releases the lines. It charges them to no budget and stops retaining when its reassembly budget drops bytes. Which error events count as terminal stays in the gateway too.

## 4. Allowed imports and the guard

| Package | May import |
| --- | --- |
| `probe` | `devshard/state`, `devshard/user`, standard library |
| `settlement` | `devshard/state`, `devshard/types`, standard library |
| `settlementwire` | `settlement`, `github.com/productscience/inference/x/inference/types`, `google.golang.org/protobuf/encoding/protowire`, standard library |
| `streamevidence` | `common/completionapi`, standard library |
| `timeoutclaim` | `devshard/host`, `devshard/types`, `devshard/user`, standard library |

Forbidden everywhere in the tree: `devshard/cmd/...`, `sync`, `sync/atomic`, `net/http`, `os`, `log`, `log/slog`, `devshard/logging`, `devshard/observability`, `github.com/prometheus/...`.

`devshard/gatewayprotocol/guard_test.go` checks both directions:

1. Every directory under the tree with production Go files has a row in the layer table and a `README.md`, every row has such a directory, and each import is in its row or the standard library and not forbidden.
2. Every production `.go` file of the `devshard` module that imports `devshard/gatewayprotocol/...` is under `devshard/cmd/gateway/...` or in the tree itself.

The reverse check keeps host packages from importing the tree directly. It does not catch a host binary that imports a `devshard/cmd/gateway` package; `go list -deps ./cmd/devshardd ./cmd/devshard-host | grep gatewayprotocol`, run from `devshard`, prints nothing while hosts do not link the tree.

## 5. Changing a rule

A protocol change is made in the tree, with a test that pins the bytes or the decision. The gateway picks it up for the parts it calls, which each package README lists.
