# `gatewayprotocol` — protocol rules shared by the gateways

Two gateways talk to the same hosts and the same chain: `devshard/cmd/devshardctl` and `devshard/cmd/gateway`. A rule lives in this tree when a host, a verifier or the chain would reject, pay differently or judge differently if it received the other gateway's result. Everything else is gateway-local and stays in its gateway: locking, memory ownership, panic recovery, HTTP, logging, metrics, and choices no other party can tell apart, such as a byte layout the chain decodes to the same message.

A protocol change is made here, once. Each package's README says how the two gateways use it. A gateway that needs a rule to behave differently changes the rule here or argues that it is local and moves it out.

## Layers

| Package | Owns | May import |
| --- | --- | --- |
| [`probe`](./probe/README.md) | the token budget of a probe: its reservation and its declared input length | `devshard/state`, `devshard/user`, standard library |
| [`settlement`](./settlement/README.md) | the settle message, the slot order of its lists, the signatures it posts, the settlement root | `devshard/state`, `devshard/types`, standard library |
| [`settlementwire`](./settlementwire/README.md) | the mapping from the settle message to `MsgSettleDevshardEscrow` and its marshalling | `settlement`, `github.com/productscience/inference/x/inference/types`, standard library |
| [`streamevidence`](./streamevidence/README.md) | which lines of an error stream are evidence, the `[DONE]` test, the error-miss payload they serialize into, the most of them a gateway retains, and whether that payload proves the error | `common/completionapi`, standard library |
| [`timeoutclaim`](./timeoutclaim/README.md) | the choice between an error-miss claim and a timeout vote, the Finish a claim carries, and the timeout vote's payload | `devshard/host`, `devshard/types`, `devshard/user`, standard library |

Forbidden in every package of the tree: `devshard/cmd/...`, `sync`, `sync/atomic`, `net/http`, `os`, `log`, `log/slog`, `devshard/logging`, `devshard/observability`, `github.com/prometheus/...`. The tree holds rules, not runtime: state, transport and observability belong to the gateway that calls it.

## Who may import the tree

Only `devshard/cmd/gateway/...`, `devshard/cmd/devshardctl` and the tree itself. Hosts never link it: `devshardd` and `devshard-host` do not import it, directly or through a gateway package, which `go list -deps ./cmd/devshardd ./cmd/devshard-host | grep gatewayprotocol` shows by printing nothing.

## The guard

`guard_test.go` enforces both directions. Every directory with production Go files has a row in its layer table and a `README.md`, every row has such a directory, and each import is in the row or the standard library and not forbidden. Every production file of the `devshard` module that imports the tree is under one of the allowed importers. The guard does not see a host binary that imports a gateway package; the `go list -deps` check above covers that.
