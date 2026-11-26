# `probe` — the token budget of a probe

## What it owns

- `MaxTokens`: the output reservation of a probe, `state.MinReservationTokens`, the smallest one the state machine accepts in a `MsgStartInference` and the executor accepts in a payload. A burned nonce stays pending and is paid its whole reservation at settlement, so every token past the floor is paid for nothing.
- `Params`: the params of a probe for a model and a start time, reserving `MaxTokens` and declaring the prompt's byte length as the input length. The state machine reserves the cost of both numbers, and the executor refuses a payload whose input length is not the prompt's byte length or whose prompt asks for more than `MaxTokens`.

## How the gateways use it

`devshard/cmd/devshardctl` builds every probe it sends or burns, the PoC probe, the ghost probe and the escrow warm-up, with `Params` (`ghostProbeParams`). `devshard/cmd/gateway` builds the warm-up probe (`warmup`) and the ghost burn (`registry`) with it. Each formats its own probe prompt with `MaxTokens` as the prompt's `max_tokens`.

## What it does not own

The probe prompt: a host accepts any prompt that parses and asks for no more than `MaxTokens`, so each gateway keeps its own literal. When a probe or a ghost burn is sent, and how it is settled.

## Imports

`devshard/state`, `devshard/user` and the standard library.
