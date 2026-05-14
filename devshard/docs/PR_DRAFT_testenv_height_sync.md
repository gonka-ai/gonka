# Draft PR: testenv + height sync (basics)

## Summary

**Draft PR.** This branch adds **`devshard/testenv`** scaffolding and **first-cut implementation** of the **height-sync (structured HTTP body, sync-turn anchors / Strong on disagreement)** direction from [`devshard/docs/proposals/HEIGHT_SYNC_HEADERS_PROPOSAL.md`](./proposals/HEIGHT_SYNC_HEADERS_PROPOSAL.md), plus the **basics of protocol-oriented testing** against that lab.

**Height sync is not complete** in this branch; the PR is meant to land **foundations** (testenv, harness direction, early behavior/tests) ahead of the full normative protocol.

---

## Design context (proposals)

Implementation and tests are aligned with or anticipate these proposals (not all are fully implemented here):

| Document | Role in this PR |
|----------|------------------|
| [`HEIGHT_SYNC_HEADERS_PROPOSAL.md`](./proposals/HEIGHT_SYNC_HEADERS_PROPOSAL.md) | **Primary:** two-section HTTP body, Anchor cadence (sync-turn windows), Strong when `abs(H_peer − H_local) > D`, forced sync turns, deferred verification — **partially implemented / tested**. |
| [`TESTENV_PROPOSAL.md`](./proposals/TESTENV_PROPOSAL.md) | **Lab stack:** self-contained compose, mock chain / dapi-shaped deps, stub engines — **basic testenv** in this tree. |
| [`PROTOCOL_TESTING_PROPOSAL.md`](./proposals/PROTOCOL_TESTING_PROPOSAL.md) | **Testing strategy:** multi-process, real HTTP, faults, assertions across user + hosts — **early steps** (Go harness / scenarios; full Python/pytest layer as future work). |
| [`CONTAINER_E2E_PLAN.md`](../testenv/scenarios/CONTAINER_E2E_PLAN.md) | **Concrete plan** to re-run height-sync E2E on the **real Docker stack** (`heightsyncd`, `mockdapi`, etc.) vs in-process `httptest` — **this PR moves in that direction**; not all scenarios from the plan are done. |
| [`CPOC_PROTOCOL.md`](./proposals/CPOC_PROTOCOL.md) | **Related:** cPoC / manual-force hooks share the same **forced sync-turn** framing; harness shape should stay compatible — **not fully delivered** here. |
| [`OBSERVABILITY_PROPOSAL.md`](./proposals/OBSERVABILITY_PROPOSAL.md) | **Related:** metrics/logs/traces in testenv; container plan references **platform health** checks — **as far as current compose wiring goes**. |
| [`VALIDATION_PROTOCOL_RANDOMNESS_PROPOSAL.md`](./proposals/VALIDATION_PROTOCOL_RANDOMNESS_PROPOSAL.md) | **Related:** production-like skew vs malice; container plan §12 — **foundational only** in this draft. |

---

## What this PR contains (high level)

- **Testenv:** compose-oriented **devshard** lab (mock chain / dapi-shaped services, hosts, proxy) so subnet-style protocol work can run **without** a full live chain or Testermint.
- **Height sync (basics):** early wiring for **Anchor / Omit / Strong** classification, sync-turn scheduling concepts, and integration with the **real** block-oracle / SSE path where the plan calls for it (vs pure in-process fakes).
- **Protocol testing:** Go-side **scenario** and/or **container** tests that exercise the stack as deployed (see [`heightsync_anchor_e2e_test.go`](../testenv/scenarios/heightsync_anchor_e2e_test.go) and [`CONTAINER_E2E_PLAN.md`](../testenv/scenarios/CONTAINER_E2E_PLAN.md) for intended coverage and gaps).

---

## Out of scope / follow-ups

- Full **normative** height-sync behavior (all edge cases, forced turns, provenance, and full container parity with every in-process scenario).
- Complete **cPoC**, **validation randomness**, and **observability** suites as specified in the linked proposals.
- Declarative **Python** driver and full **fault-injection** surface described in `PROTOCOL_TESTING_PROPOSAL.md` (unless explicitly included in this branch’s diff).

---

## How to review / run

- **Docs:** start with [`HEIGHT_SYNC_HEADERS_PROPOSAL.md`](./proposals/HEIGHT_SYNC_HEADERS_PROPOSAL.md) and [`CONTAINER_E2E_PLAN.md`](../testenv/scenarios/CONTAINER_E2E_PLAN.md).
- **Testenv:** [`devshard/testenv/README.md`](../testenv/README.md) (and scenario docs under [`devshard/testenv/scenarios/`](../testenv/scenarios/)).
- **Tests:** run the scenario / integration tests documented for this branch (container tests may need Docker and build tags as in existing `citest` / compose patterns).

---

## Changed paths (fill in before merge)

_Add a short list of directories/files once the diff is final, e.g. `devshard/testenv/`, `devshard/cmd/devshardctl/`, `devshard/docs/proposals/`, `*_e2e_test.go`._
