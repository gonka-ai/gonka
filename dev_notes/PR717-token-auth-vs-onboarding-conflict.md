# PR #717 (token-auth v2) ↔ Onboarding PR #1296 — conflict analysis & path forward

_Date: 2026-06-17 · branch under review: `onboarding_clarity_1261` (PR #1296)_

## TL;DR

The two PRs collide on **MLNode addressing**. #717 changes *how every MLNode is
reached* (BaseURL/FQDN + bearer token instead of Host+Port); onboarding adds new
admin endpoints that *reach MLNodes the old way*. Result is one compile-break and
one silent semantic break. The clean fix is not "rebase and patch the call site"
— it's to **centralize MLNode addressing+auth behind one seam** so neither PR
(nor the next one) has to know the wire details.

---

## PR #717 — current state

- Branch `jf/token-auth-v2` → base `upgrade-v0.2.12`. **OPEN**, GitHub marks it
  `CONFLICTING` (already conflicts with its own base → needs a rebase regardless).
- 34 files. Core changes:
  - `ClientFactory.CreateClient(pocUrl, inferenceUrl)` → **4 params**
    `(pocUrl, inferenceUrl, authToken, baseURL)`.
  - URL construction: when `BaseURL` is set, use `<baseURL>/<version>/<path>`
    instead of `http://<host>:<port>/<segment>`; new helpers on `broker.Node`
    (`BaseUrlWithVersion`, `GetMlNodeUrl`, and reworked
    `InferenceUrlWithVersion`/`PoCUrlWithVersion`).
  - Inject `Authorization: Bearer <token>` on all MLNode requests when set.
  - `InferenceNodeConfig` and `broker.Node` gain `BaseURL` + `AuthToken`
    (additive; SQLite-only, not on-chain). New `base_url`/`auth_token` columns
    with auto-migration.
  - Health endpoint varies by registration mode: legacy →
    `<inference_port>/health`; baseURL → `<baseURL>/readyz`.
- **Unresolved review threads** (not blockers for *this* conflict, but pending):
  - `post_chat_handler.go` forwarding path uses `context.Background()` +
    `http.DefaultClient` → drops timeout/cancellation; DimaOrekhovPS also notes
    json-marshal errors are now wrapped as *retryable transport* errors instead
    of non-retryable application errors. Re-marshal via `map[string]interface{}`
    can change payload (number types/ordering/escaping).
  - Copilot: whitespace not trimmed in `formatURLWithVersion` /
    `formatBaseURLWithVersion` / `broker.go` URL builders.
  - `node_admin_commands.go:125` dot-heuristic rejects single-label hosts
    (`http://mlnode:8080`); author says intentional.
  - mock-server `AuthTokenService` host-matching may never match default entry;
    `InferenceRoutes.kt` logs full Authorization value (token leak in logs).

## Onboarding PR #1296 — current state

- `[P2] Onboarding Clarity: handler-layer UX + selfcheck (supersedes #1261, #866)`
  → base `upgrade-v0.2.14`. Onboarding-specific commits touch **5 files**, all in
  `decentralized-api/internal/server/admin/`:
  - `fact_handlers.go` (+ test), `fact_endpoints_smoke_test.go` — new read-only
    fact endpoints: `GET nodes/:id/test`, `GET nodes/:id/launch-plan`,
    `GET poc/timing`.
  - `mlnode_tester.go` — one-shot MLNode validation (`MLNodeTester`).
  - `server.go` — +5 lines registering the three GET routes.

The collision point is `mlnode_tester.go` `runOnce`:

```go
version := t.configManager.GetCurrentNodeVersion()
pocUrl := apiconfig.MLNodeURL(cfg.Host, cfg.PoCPort, cfg.PoCSegment, version)
inferenceUrl := apiconfig.MLNodeURL(cfg.Host, cfg.InferencePort, cfg.InferenceSegment, version)
client := t.factory.CreateClient(pocUrl, inferenceUrl)   // ← 2 args
```

---

## The conflicts

**1. Compile-break — `CreateClient` signature.**
#717 makes `CreateClient` 4-arg; the tester calls it with 2. Whoever lands second
won't compile until this call is updated. Same class of break #717's own Copilot
review already flagged for `broker_test.go`.

**2. Semantic break — tester bypasses BaseURL/AuthToken (the dangerous one).**
The tester builds URLs from `Host/Port/Segment` via `apiconfig.MLNodeURL` and
creates a client with **no auth token**. After #717 lands, for a node registered
the new way (BaseURL-only, AuthToken set):
- `cfg.Host` is empty → wrong/empty URL,
- no `Authorization` header.

So `POST/GET nodes/:id/test` and `GET nodes/:id/launch-plan` would **falsely FAIL
or hit the wrong endpoint** precisely for the cloud deployments (Aliyun EAS, etc.)
#717 exists to support. Onboarding self-check breaks exactly where token-auth is
in use. This is invisible to a git merge — it only surfaces at runtime.

**3. Textual overlap — low risk.**
Both touch `admin/`, but onboarding edits `server.go` route registration while
#717 touches `setup_report.go`/`node_handlers.go`/`server_test.go` — different
spots. `InferenceNodeConfig` gains fields additively (`DeepCopy` carries them),
no struct conflict.

---

## What is #717 *actually* solving?

Strip the 34 files down and #717 is solving **one** problem:

> The way an API node addresses and authenticates to an MLNode is hardcoded to
> `(Host, Port, Segment)` with no auth, and that assumption is duplicated in
> every call site.

Two concrete needs:
1. **Stable addressing** — cloud providers reassign IPs on container recreation;
   a stable FQDN/BaseURL must work in place of Host+Port.
2. **Authenticated access** — managed MLNode services sit behind a bearer token.

The reason it's a 34-file PR (and why it conflicts with *anything* touching MLNode
access, including onboarding) is that **URL construction + client creation is
scattered**: `broker.go`, `mlnode_background_manager.go`, `setup_report.go`,
`mlnodeclient`, and now onboarding's `mlnode_tester.go` each hand-build
`http://host:port/segment` and call `CreateClient(pocUrl, inferenceUrl)`. There is
no single seam that owns "given this node + version, give me a ready client."
Every new feature that talks to an MLNode re-implements addressing → every such
feature is a future merge conflict with #717.

---

## How to do it without conflict

### Recommended — extract the addressing/auth seam first (kills the conflict class)

Introduce **one** abstraction that owns MLNode addressing + auth, land it into the
shared base (`upgrade-v0.2.14`) independent of both PRs, then have both build on it:

- Add a factory method like
  `CreateClientForNode(node /* config or broker.Node */, version) MLNodeClient`
  (or a small `NodeEndpoint`/`NodeAddress` type that encapsulates URL building +
  auth header). All current call sites switch to it — a mechanical refactor with
  **no behavior change** (Host/Port path only, AuthToken="" / BaseURL="").
- Then:
  - **Onboarding** rebases to call `CreateClientForNode(cfg, version)` instead of
    `apiconfig.MLNodeURL(...)` + 2-arg `CreateClient`. It becomes auth/baseURL
    agnostic *for free* — both conflicts (1) and (2) disappear.
  - **#717** shrinks dramatically: it only extends the config struct + SQLite and
    implements baseURL/token logic *inside the seam*, instead of editing every
    call site. Far smaller, far less conflict-prone, easier to review.

This is the only option that prevents the *next* MLNode-touching PR from hitting
the same wall.

### Pragmatic minimum — sequence + checklist

If the seam refactor isn't in scope now:

- Merge **onboarding first** (smaller, P2, nearly done; #717 is bigger, still
  CONFLICTING, has open review threads).
- When #717 rebases forward past onboarding, it **must** update `mlnode_tester.go`:
  1. pass `cfg.AuthToken`, `cfg.BaseURL` into the 4-arg `CreateClient`;
  2. build URLs with the new BaseURL-aware helper (`broker.GetMlNodeUrl` /
     `BaseUrlWithVersion`) instead of `apiconfig.MLNodeURL`, so the tester respects
     baseURL registration.
- Leave a comment on #717 listing `mlnode_tester.go` as a required touch-point so
  the semantic break isn't forgotten (a git merge will not surface it).

### Not recommended

Merging #717 first and having onboarding build on the new API — blocks the smaller,
ready PR on the bigger conflicting one with unresolved review.

---

## Action items

- [ ] Decide: seam-refactor-first (preferred) vs. sequence-and-patch.
- [ ] If sequencing: comment on #717 flagging `mlnode_tester.go` as a required
      rebase touch-point (4-arg `CreateClient` + BaseURL-aware URL helper).
- [ ] Either way: when #717 lands, add a test for the fact/test endpoints against a
      BaseURL+AuthToken node so the semantic break can't regress silently.

---

## Seam refactor inventory (2026-06-17)

_Scope for the `CreateClientForNode` seam, scanned on branch `mlnode-token-auth`._
_All paths under `decentralized-api/`._

There are **three layers** auth+baseURL must flow through. Today each is scattered.

### Layer 1 — client creation (`CreateClient`, 2-arg → 4-arg)

Production call sites (4):
- `broker/broker.go:447` — `b.mlNodeClientFactory.CreateClient(node.PoCUrlWithVersion(v), node.InferenceUrlWithVersion(v))` — **the main one**, used for all broker inference/management.
- `broker/node_worker.go:137` — `factory.CreateClient(pocUrl, inferenceUrl)` (versioned client for rolling upgrade).
- `internal/modelmanager/mlnode_background_manager.go:130` and `:294`.
- `internal/server/admin/mlnode_tester.go:348` — **onboarding's; the conflict point**.

Factory definition: `mlnodeclient/client_factory.go:6,11,26` (interface + Http + Mock impls).
Constructor: `mlnodeclient/client.go:32` `NewNodeClient(pocUrl, inferenceUrl)`.

Test/mock call sites to update (≈14): `broker/node_worker_test.go:328`,
`broker/broker_test.go:758`, `modelmanager/mlnode_background_manager_test.go:77`
(mock impl), `admin/mlnode_tester_test.go` (×9), `admin/node_handlers_test.go:88,485`,
`admin/server_test.go:210`, `public/post_chat_handler_interruption_test.go:527`,
`event_listener/integration_test.go:461`.

### Layer 2 — URL construction (node → pocURL/inferenceURL)

Single low-level builder (good): `apiconfig/mlnode_url.go:11` `MLNodeURL(host, port, segment, version)`
→ `http://<host>:<port>[/<version>]<segment>`. **This is the one place that needs
the baseURL branch added** (`if baseURL != "" → <baseURL>[/<version>]<path>`).

But the **node→URL mapping is duplicated 4×** (all call `MLNodeURL` with Host/Ports,
ignoring any baseURL):
- `broker/broker.go:173-186` — `Node.InferenceUrl / InferenceUrlWithVersion / PoCUrl / PoCUrlWithVersion`.
- `internal/modelmanager/mlnode_background_manager.go:195-230` — `getPoCUrl(WithVersion)`,
  `getInferenceUrl(WithVersion)`, `formatURLWithVersion`.
- `internal/server/admin/setup_report.go:882-902` — **same trio, copy-pasted**.
- `internal/server/admin/mlnode_tester.go:346-347` — inline `MLNodeURL(cfg.Host, …)` ×2.

Two node representations carry the address fields:
- `apiconfig.InferenceNodeConfig` (`apiconfig/config.go:121`) — Host/Segments/Ports.
- `broker.Node` (`broker/broker.go:160`) — same fields.
Both need `BaseURL`/`AuthToken` added; both flow into the seam.

### Layer 3 — Authorization header injection (the actual requests)

Most MLNode traffic goes through `mlnodeclient` via `utils.Send{Get,PostJson,DeleteJson}Request(ctx, &client, url, …)`:
- `mlnodeclient/client.go` (×6), `models.go` (×5), `gpu.go` (×2), `poc_v2_requests.go` (×4).
- These take the client's `http.Client`; the token must be threaded to the client
  (e.g. store on `Client`, add header in the `utils.Send*` path — #717 added
  `SendPostJsonRequestWithAuth` etc. in `utils/http.go`).

**Direct calls that BYPASS the client** (also need the header, easy to miss):
- `internal/server/public/post_chat_handler.go:420` — `http.NewRequest` forward + `:437` `s.httpClient.Do` (chat-completions forwarding; this is the file with #717's unresolved `context.Background()`/`http.DefaultClient`/error-wrapping review).
  Also builds URLs at `:501,:611` via `node.InferenceUrlWithVersion(...)`.
- `internal/validation/inference_validation.go:920` — builds URL via `InferenceUrlWithVersion`, posts chat completions.
- `internal/devshard/engine.go:60` and `internal/devshard/validation.go:70` — `http.NewRequestWithContext` against `node.InferenceUrlWithVersion(...) + "/v1/chat/completions"`.

### What the seam should look like

1. Add `BaseURL`/`AuthToken` to **both** `InferenceNodeConfig` and `broker.Node` (additive).
2. Make `apiconfig.MLNodeURL` baseURL-aware (one branch) — single source of truth for URL shape.
3. Collapse the duplicated trio (`broker.Node` methods, background_manager, setup_report, mlnode_tester) onto **one** node→URL path.
4. `ClientFactory.CreateClientForNode(node, version)` (or 4-arg `CreateClient(poc, inf, authToken, baseURL)`) — builds URLs + carries the token. All Layer-1 sites switch to it.
5. Thread the token through `utils.Send*` (client path) **and** patch the 4 bypass sites (post_chat_handler, inference_validation, devshard ×2).

Step (3)+(4) with empty BaseURL/AuthToken = **zero behavior change** → safe standalone "seam" commit/PR. Step (1)+(2)+(5) = the actual feature on top.

Rough size: ~4 prod client sites + ~14 test sites + 4 duplicated URL helpers + 4 bypass HTTP sites + 2 structs + 1 URL builder. Confirms #717's 34-file spread is mostly *scatter*, not essential complexity.
