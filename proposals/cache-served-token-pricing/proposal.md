# Proposal: Cache-Served Token Pricing

**Author:** Aung Myat Moe — operator of the Fusion gateway (`api.fusioncode.app`), a broker serving `deepseek-ai/DeepSeek-V4-Flash-0731`, `MiniMaxAI/MiniMax-M2.7`, and `moonshotai/Kimi-K2.6` through the Gonka network.

**Status:** Proposal — open for discussion and feedback.
**Category:** Tokenomics / Inference pricing.

## Summary

The network already performs real per-host KV prefix caching, but GNK is billed
flat per token — cached tokens cost the same as fresh ones. This proposal makes
**cache-served tokens bill at a reduced rate**, so the network's actual hardware
savings flow to brokers and their clients, and the network wins more
cache-heavy workloads (agents, long stable system prompts) that currently go to
centralized providers with native cache discounts (e.g. DeepSeek's ~90% cached-
input price).

## Motivation

Live measurement on 2026-08-24 against `api.openbroker.gonka.gg`
(`deepseek-ai/DeepSeek-V4-Flash-0731`, 52k-token prompt):

| Request | Latency | `prompt_tokens_details` |
|---|---|---|
| Cold prefix | 12–31 s | `null` |
| Changed suffix (same host) | ~5 s | `null` |
| Exact replay (same host) | 23–41 ms | `null` |

The latency collapse proves vLLM is reusing the GPU KV cache per host. Yet GNK
billing is flat: **15 nGNK per token for every token**, cached or not (verified
from the OpenBroker usage API and ledger). Downstream brokers therefore cannot
offer cache-based pricing, so the network is structurally uncompetitive for
cache-heavy traffic — the exact traffic DeepSeek's own API discounts ~90%.

Fixing this has three prerequisites, one of which is already in flight:

1. **Telemetry** — make vLLM report cache hits
   (`--enable-prefix-caching` + `--enable-prompt-tokens-details`, so
   `usage.prompt_tokens_details.cached_tokens` is populated). In review as
   PR #1633.
2. **Accounting** — the Finish payload must carry the cached-token count so it
   can be validated and billed (extends the token counts self-reported on
   Finish, see #1474).
3. **Pricing** — this proposal: bill the cached portion of each request at a
   discounted per-token rate.

## How the discount is earned (why hosts still profit)

Host cost is dominated by **prefill** — attention over every token. A KV-cache
hit means the prefill was already computed: the host spends ~0 compute on
cached tokens and only generates the new completion. Cached tokens are nearly
pure revenue. A discounted cached rate still pays the host *more than idle
GPU time* and increases utilization, so hosts are economically better off —
the discount is a utilization incentive, not a loss.

## Proposed pricing model

Per-request GNK cost becomes:

```
cost = input_rate × fresh_input_tokens
     + cached_rate × cached_tokens
     + output_rate × completion_tokens
```

- `cached_rate = input_rate × DISCOUNT` (proposed default `DISCOUNT = 0.10`,
  i.e. cached tokens cost 10% of fresh — mirroring DeepSeek's cached-input
  economics; negotiable per model).
- Exact-response replays deduplicated at the gateway already cost zero
  upstream and are unaffected.
- Only provider-reported `prompt_tokens_details.cached_tokens` counts are
  credited — never locally estimated values (the estimate is a stopgap for
  client visibility only, not a billing input).

With today's flat rate of 15 nGNK/token and `DISCOUNT = 0.10`:

| Component | nGNK/token (example) |
|---|---|
| Fresh input | 15.0 |
| Cached input | 1.5 |
| Output | 15.0 (unchanged) |

A request with 50k cached + 2k fresh input + 16 output tokens drops from
`780,240 nGNK` to `82,740 nGNK` — an ~89% reduction, matching the real
hardware savings.

## Implementation options

### Option A — Gateway-level (fast, no chain change)

The public gateway (OpenBroker / brokers) already receives
`prompt_tokens_details.cached_tokens` once PR #1633 lands. The gateway applies
the discount when computing the GNK deduction for each request. Pro:

- Ships in days, no chain upgrade, no validation change.
- Host payout and broker billing both reflect the discount immediately.

Con:

- Discount policy lives in gateway code, not on-chain — less auditable and
  could diverge between gateways/brokers.

### Option B — On-chain (structural, auditable)

Extend the inference Finish/validation payloads (input/output counts today,
see #1474 and `common/validation/validation.go`) with a `cached_tokens` count.
`inference-chain` billing (see `x/inference/epochgroup/unit_of_compute_price.go`)
then computes the discounted cost. Pro:

- Single source of truth, validated, all brokers and hosts see the same rule.
- Enables per-model discount factors via existing epoch/params mechanisms.

Con:

- Requires chain upgrade + validation changes (validation must bound
  `cached_tokens ≤ prompt_tokens` and detect inflated cache claims, in the
  spirit of the existing `TokenCountInflated` checks).

**Recommended path:** start with Option A to capture the economics now, then
land Option B as the durable protocol rule.

## Open questions

1. **Trust** — cached counts are host-reported (like token counts today).
   Should validation re-check cache claims (e.g. reject `cached_tokens` when
   the validator's own re-execution saw no shared prefix)? Same risk class as
   #1474.
2. **Discount factor** — fixed 0.10, or per-model
   (models with cheaper prefill → smaller discount)? Should it be governed via
   epoch params?
3. **Minimum cacheable prefix** — DeepSeek counts cache only above a minimum
   cached length; should the network too, to avoid tiny-prefix gaming?
4. **Host payout floor** — hosts still pay electricity/bandwidth per request;
   is there a per-request minimum so cached-only requests never cost hosts
   money?
5. **Interplay with dynamic pricing** — how does this interact with the
   tokenomics-v2 dynamic-pricing proposal (per-model utilization-based price)?

## Impact

- **Who is affected:** all brokers and developers using the OpenBroker/Gonka
  API; hosts (payout per token); downstream billing systems.
- **Network-wide or limited:** network-wide — pricing applies to every
  request.
- **Likelihood:** common — cache hits already occur today (verified
  per-host); the discount simply prices them correctly.
- **Severity (Impact × Likelihood):** Medium-High — affects revenue for hosts
  and cost for brokers, but is a pure pricing change with no service risk.
- **Affected components:** inference-chain tokenomics
  (`x/inference`), gateway/broker billing, validation
  (`common/validation`), mlnode vLLM flags (PR #1633).

## Who this helps

- **Brokers** — pass real cache discounts to clients, compete with
  centralized providers on cache-heavy workloads.
- **Clients** — agents and tools with stable system prompts pay closer to
  what the hardware actually costs.
- **Hosts** — higher utilization from the workloads the discount attracts,
  earning more total GNK despite the per-token discount.
- **The network** — a pricing moat: centralized DeepSeek does this; a
  decentralized network that prices cache honestly wins the same workloads
  while keeping its other per-token advantages.