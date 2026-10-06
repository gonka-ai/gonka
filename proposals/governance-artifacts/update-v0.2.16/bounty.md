# Bounty Payments Associated with v0.2.16

This proposal covers 104,150 USDT from community funds for development, research, infrastructure, security reporting, and code review work on the Gonka protocol. The work includes contributions to upgrade v0.2.16 and larger projects spanning multiple upgrade cycles.

## 1. Decode-PoC: 38,000 USDT.

Decode-PoC moves from prefill-only proof of compute covering token generation, aiming to make measured compute capacity better align with real inference workloads. Axel-t published the [original proposal](https://github.com/gonka-ai/gonka/issues/1135) in April 2026.

The work covers CUDA graph support, vLLM porting, and testing across hardware configurations and models.

Axel-t: 18,000 USDT for CUDA graph support. Technical details are available in the [PoC-decode documentation](https://github.com/axeltec-software/vllm/blob/poc-v0.20-decode-poc-cg/benchmarks/poc/POC_DECODE.md).

- Previously paid: 30,000 USDT on June 17, 2026, through governance proposal 76, for the scheme's design and experimental feasibility validation.

- Upon completion, a final bounty of 12,000 USDT for Axel-t's contribution to the vLLM port, test suite, and on-chain integration will be proposed together with the upgrade that integrates decode-PoC on-chain. Both the previous payment and the planned final bounty are outside this proposal's total.

Kaitaku.ai: 20,000 USDT for porting, experiments, validation, and integration work. The [initial report](https://github.com/kaitakuai/experiments/blob/main/reports/2026-06-decode-poc-research-and-integration.md) covers June 9-September 10, 2026, with [continued integration work](https://github.com/kaitakuai/experiments/blob/main/reports/2026-09-decode-poc-glm-028-and-0300-migration.md) documented through September 25, 2026.

## 2. gonka-poc plugin: 5,000 USDT.

This project packages PoC functionality as a vLLM plugin, reducing the work needed to support new vLLM releases.

Kaitaku.ai: 5,000 USDT for plugin development and integration. The [report](https://github.com/kaitakuai/experiments/blob/main/reports/2026-07-gonka-poc-plugin-and-residual.md) covers June 16-September 8, 2026.

## 3. Model benchmarking and integration: 13,000 USDT.

This work prepares candidate models for use on Gonka. It includes necessary vLLM updates, PoC benchmarks across common GPU types, and testing validation thresholds to detect model substitution while allowing different GPU types to validate each other. It also includes on-chain proposals with model parameters and coefficients.

Kaitaku.ai:

- DeepSeek-V4-Flash: 5,000 USDT for experiments, a vLLM update, an on-chain model proposal, and additional experiments for the 0731 version.

- GLM-5.3-Flash: 4,000 USDT for experiments, a vLLM update, and an on-chain model proposal.

- GLM-5.2: 2,000 USDT for experiments and deployment images for B300, B200, and H200.

- Hy3: 2,000 USDT for experiments. This initial evaluation did not result in an on-chain proposal.

> The proposed amounts use the following breakdown: 2,000 for a set of experiments; 1,000 for a vLLM update associated with a new model proposal; and 1,000 for preparing the on-chain proposal with model parameters and coefficients. DeepSeek includes an additional 1,000 for repeating experiments for the 0731 version, without any additional updates specific for that version.

## 4. Vulnerability reporting: 5,000 USDT.

This payment recognizes the report of a vulnerability that could halt the chain.

@vitaly-andr: 5,000 USDT for the high-severity vulnerability report [#1205](https://github.com/gonka-ai/gonka/issues/1205), with a proposed fix in [cosmos-sdk#16](https://github.com/gonka-ai/cosmos-sdk/pull/16).

## 5. Trainshards: 7,000 USDT.

This work contributes to the development of the network's training capabilities, including contributions [#1350](https://github.com/gonka-ai/gonka/pull/1350) and [#1618](https://github.com/gonka-ai/gonka/pull/1618).

@x0152: 7,000 USDT for eight weeks of part-time Trainshards v0 development.

## 6. ML-node observability: 12,500 USDT.

This project provides an ML-node metrics exporter, a DAPI federation endpoint, and Grafana dashboards for monitoring node performance, together with hosting and maintenance.

Kaitaku.ai: 12,000 USDT, comprising 4,000 for development and 8,000 for hosting and maintenance, with a commitment to continued hosting and support. The [report](https://github.com/kaitakuai/experiments/blob/main/reports/2026-07-mlnode-observability.md) covers July 6-September 8, 2026. Grafana hosting began in July, and the public community instance launched on September 4.

@qdanik: 500 USDT for contributions to the same project.

## 7. GPU rental reimbursement: 9,600 USDT.

This covers GPUs rented for model and PoC experiments across different hardware configurations since June 1, 2026.

Kaitaku.ai: 9,600 USDT for GPU rental expenses related to model and PoC experiments. Details of the experiments and GPU configurations are available in [Kaitaku.ai's technical reports](https://github.com/kaitakuai/experiments/tree/main/reports) covering June-September 2026.

*These costs are listed separately from Kaitaku.ai's development and research bounties. Other teams' or projects' bounty amounts may already include their GPU costs.*

## 8. Security reviews, upgrade reviews, fixes, and release management: 14,050 USDT.

This work covers assessment of incoming security reports, review of the v0.2.16 upgrade, and fixes addressing identified issues.

@staaason / @zpoken: 8,050 USDT jointly for reviewing 115 HackerOne reports and delivering fixes [#1767](https://github.com/gonka-ai/gonka/pull/1767) and [#1552](https://github.com/gonka-ai/gonka/pull/1552), the latter incorporated into [#1623](https://github.com/gonka-ai/gonka/pull/1623). The review work covered initial assessment of approximately 50% of incoming HackerOne reports over six weeks. The fixes address genesis-transfer status reporting and PoC submission permissions.

@vitaly-andr: 1,000 USDT for upgrade review with valuable actionable comments.

@bonujel: 1,000 USDT for upgrade review with valuable actionable comments.

@cyberdelamain: 3,000 USDT, comprising 1,000 for upgrade review and 2,000 for fixes [#1828](https://github.com/gonka-ai/gonka/pull/1828), [#1846](https://github.com/gonka-ai/gonka/pull/1846), [#1845](https://github.com/gonka-ai/gonka/pull/1845), and [#1859](https://github.com/gonka-ai/gonka/pull/1859).

@x0152: 1,000 USDT for upgrade review work under release management.

The payout schedule below is proposed separately from the v0.2.16 software upgrade.

## Payout Schedule

Total: 104,150 USDT from community-sale funds.

| Bounty | GitHub | Amount (USDT) | Explanation | Address |
|---|---|---|---|---|
| ML-node observability | kaitaku.ai (@baychak, @clanster) | 12,000 | Development 4,000: ML-node metrics exporter, DAPI federation endpoint, Grafana dashboards, public repo [gonka-grafana](https://github.com/kaitakuai/gonka-grafana). Hosting and maintenance 8,000: [monitoring.kaitaku.ai](https://monitoring.kaitaku.ai), with a commitment to continue. [Report](https://github.com/kaitakuai/experiments/blob/main/reports/2026-07-mlnode-observability.md) | gonka1x45hruazmcqxslj3g8a08988hr5fr3wx33drhp |
| ML-node observability | @qdanik | 500 | Contribution to the same ML-node observability track. [Report](https://github.com/kaitakuai/experiments/blob/main/reports/2026-07-mlnode-observability.md) | gonka1j3f2xkapx8cmczpjqcsrh7cc3peyj3ngkjv4p8 |
| decode-PoC experiments | kaitaku.ai (@baychak, @clanster) | 20,000 | vLLM port and experiments across hardware configurations and models. [Report](https://github.com/kaitakuai/experiments/blob/main/reports/2026-06-decode-poc-research-and-integration.md) | gonka1x45hruazmcqxslj3g8a08988hr5fr3wx33drhp |
| decode-PoC experiments, CUDA graphs | Axel-t | 18,000 | CUDA graph support for decode-PoC experiments. [Report](https://github.com/kaitakuai/experiments/blob/main/reports/2026-06-decode-poc-research-and-integration.md) | gonka1yhdhp4vwsvdsplv4acksntx0zxh8saueq6lj9m |
| gonka-poc plugin | kaitaku.ai (@baychak, @clanster) | 5,000 | Plugin removing re-porting PoC on every vLLM release. [Report](https://github.com/kaitakuai/experiments/blob/main/reports/2026-07-gonka-poc-plugin-and-residual.md) | gonka1x45hruazmcqxslj3g8a08988hr5fr3wx33drhp |
| DeepSeek-V4-Flash experiments | kaitaku.ai (@baychak, @clanster) | 5,000 | Experiments and new model proposal. [Report](https://github.com/kaitakuai/experiments/blob/main/reports/2026-07-deepseek-v4-flash-integration.md) | gonka1x45hruazmcqxslj3g8a08988hr5fr3wx33drhp |
| GLM-5.3-Flash experiments | kaitaku.ai (@baychak, @clanster) | 4,000 | Experiments and new model proposal. [Report](https://github.com/kaitakuai/experiments/blob/main/reports/2026-09-glm-5-3-flash-integration.md) | gonka1x45hruazmcqxslj3g8a08988hr5fr3wx33drhp |
| GLM-5.2 experiments | kaitaku.ai (@baychak, @clanster) | 2,000 | Experiments and images for B300, B200, and H200. [Report](https://github.com/kaitakuai/experiments/blob/main/reports/2026-06-glm-5-2-bring-up.md) | gonka1x45hruazmcqxslj3g8a08988hr5fr3wx33drhp |
| Hy3 evaluation | kaitaku.ai (@baychak, @clanster) | 2,000 | Experiments. [Report](https://github.com/kaitakuai/experiments/blob/main/reports/2026-08-hy3-evaluation.md) | gonka1x45hruazmcqxslj3g8a08988hr5fr3wx33drhp |
| GPU rental reimbursement | kaitaku.ai (@baychak, @clanster) | 9,600 | GPU rentals for model and PoC experiments since 2026-06-01, at cost against billing. | gonka1x45hruazmcqxslj3g8a08988hr5fr3wx33drhp |
| Release management | @x0152 | 8,000 | PR reviews, HackerOne report reviews, ongoing Trainshards v0 contributions ([#1350](https://github.com/gonka-ai/gonka/pull/1350), [#1618](https://github.com/gonka-ai/gonka/pull/1618)) | gonka18enyz7h6hh5zjveee5wnhkhrcexamfz0zdxxqe |
| Chain-halt report #1205 | @vitaly-andr | 5,000 | Bug report ([#1205](https://github.com/gonka-ai/gonka/issues/1205), fix [cosmos-sdk#16](https://github.com/gonka-ai/cosmos-sdk/pull/16)). | gonka1uqt4hue8tljwwgdkvtthyl3n8kkkqtydyns4cm |
| v0.2.16 review | @vitaly-andr | 1,000 | Upgrade review. | gonka1uqt4hue8tljwwgdkvtthyl3n8kkkqtydyns4cm |
| v0.2.16 review | @bonujel | 1,000 | Upgrade review. | gonka1zqss46r6jf6dhhyaa777kc2ppvjhn0ufkx4y57 |
| HackerOne reviews | @staaason / @zpoken | 8,050 | HackerOne report reviews plus [#1767](https://github.com/gonka-ai/gonka/pull/1767) and [#1552](https://github.com/gonka-ai/gonka/pull/1552) (landed as [#1623](https://github.com/gonka-ai/gonka/pull/1623)) | gonka1s8zggm642e3kncy48c7vxwmeclt2wxyyd8qtdt |
| v0.2.16 review and fixes | @cyberdelamain | 3,000 | Upgrade review and fixes [#1828](https://github.com/gonka-ai/gonka/pull/1828), [#1846](https://github.com/gonka-ai/gonka/pull/1846), [#1845](https://github.com/gonka-ai/gonka/pull/1845), [#1859](https://github.com/gonka-ai/gonka/pull/1859) | gonka15u0r3mf6t7zsfuslusnyt7hsjrq357yumpe8st |
