# MLNode `Endpoint` seam — implementation blueprint

_Branch `mlnode-token-auth` (off `onboarding_clarity_1261`). Outcome of the
grill-with-docs session, 2026-06-17. Decisions also recorded in
`decentralized-api/CONTEXT.md` and `docs/adr/0001-mlnode-endpoint-value-object.md`._

Goal: land BaseURL/FQDN + bearer-token MLNode registration (superseding stalled
PR #717) by introducing one addressing seam instead of #717's 34-file scatter,
without breaking onboarding PR #1296.

## The value object

New leaf package `decentralized-api/mlnode` (imports nothing in-repo):

```go
type Endpoint interface {
    PoCURL(version string) string        // management API base
    InferenceURL(version string) string  // inference base (== PoCURL in BaseURL mode)
    HealthURL(version string) string     // absorbs /health (Host-Port) vs /readyz (BaseURL)
    AuthToken() string                   // raw token; "" when none
}

// two unexported implementations: hostPortEndpoint, baseURLEndpoint
// New picks the variant and enforces XOR; URL validity parsed/checked here.
func New(...primitive fields...) (Endpoint, error)
```

- `.Endpoint()` is **total** (no error) on both structs — matches today's
  infallible `MLNodeURL`. It does string assembly only, never `url.Parse`.
  Validity is checked once at the boundary (see Validation). If both modes are
  somehow set, **baseURL wins** (deterministic tiebreak).
- `apiconfig.MLNodeURL` logic **moves into** this package (it becomes the
  Host-Port variant's URL builder). apiconfig → mlnode (one-way, no cycle).
- Two layers stay separated: **A) base-URL construction = `Endpoint`, total,
  sprintf**; **B) path joining = `mlnodeclient` via `url.JoinPath`, fallible,
  unchanged.** Only the health URL moves A-ward (it is the one mode-dependent
  path).

## Terminology (in CONTEXT.md)

MLNode · InferenceNodeConfig · Node (`broker.Node`) · Endpoint (`mlnode.Endpoint`)
· Host-Port mode · BaseURL mode. Avoid `MLNodeEndpoint` (stutters).

## Implementation steps

### Step 1 — `mlnode` package (behavior-preserving)
- Create `mlnode.Endpoint` + `hostPortEndpoint` + `baseURLEndpoint` + `New(...)`.
- Move `apiconfig.MLNodeURL` shaping in; Host-Port variant reproduces it exactly
  (incl. legacy `segment`, kept, confined here, marked legacy).
- Add `InferenceNodeConfig.Endpoint()` and `broker.Node.Endpoint()`.
- With BaseURL/AuthToken empty everywhere, this is **zero behavior change**.

### Step 2 — replace the client factory (behavior-preserving)
- `mlnodeclient.CreateClient(pocUrl, inferenceUrl)` → **wholesale replace** with
  `CreateClientForNode(ep mlnode.Endpoint, version string) MLNodeClient`.
- `NewNodeClient(pocURL, inferenceURL, healthURL, authToken string)` — client
  gains `healthURL` (use it for health, drop `JoinPath(inferenceUrl,"/health")`)
  and `authToken`.
- Migrate the 4 prod call sites: `broker/broker.go:447`, `broker/node_worker.go:137`,
  `modelmanager/mlnode_background_manager.go:130,294`, `admin/mlnode_tester.go:348`.
- Migrate ~14 test/mock sites (build a test endpoint; add a `mlnode` test ctor).
- Collapse the 4 duplicated URL-helper sets (`broker.Node` methods,
  `mlnode_background_manager`, `setup_report`, `mlnode_tester`) onto `.Endpoint()`.
- Still zero behavior change (Host-Port only).

### Step 3 — feature: BaseURL + AuthToken fields
- Add `BaseURL`/`AuthToken` to `InferenceNodeConfig` and `broker.Node` (additive),
  copy them in the `RegisterNode`/`UpdateNode` mapping (`node_admin_commands.go:111,256`),
  and in `node_handlers.go` syncNodesWithConfig (the x0152 "you missed it" site).
- SQLite: `base_url`/`auth_token` columns + migration (`sqlite_store.go`).
- `New(...)` builds the right variant from the now-populated fields.

### Step 4 — boundary validation (mode-aware)
- Make `apiconfig.ValidateInferenceNodeBasic` mode-aware (delegating XOR + URL
  format to `mlnode.Validate`): host/ports required **only** in Host-Port mode;
  baseURL must be a valid HTTP(S) URL; exactly one mode set.
- `node_admin_commands` duplicate-check by mode: baseURL nodes checked for baseURL
  uniqueness, host-port nodes for host+port; modes don't false-conflict (x0152).

### Step 5 — auth injection (one rule, everywhere)
- Add `utils.SetBearerAuth(req *http.Request, token string)` (uses
  `utils.AuthorizationHeader`; "Bearer " + non-empty rule lives here only).
- Client path: route through `utils.Send*WithAuth` (which call SetBearerAuth).
- **Bypass MLNode sites** (raw `*http.Response` passthrough — justified, NOT
  folded into the typed client which has no chat-completions method):
  - `post_chat_handler.go` tokenize `:501`, completions `:611`
  - `inference_validation.go:920`
  - `devshard/engine.go:60`, `devshard/validation.go:70`
  Each: base URL via `node.Endpoint().InferenceURL(version)`; auth via
  `utils.SetBearerAuth(req, node.Endpoint().AuthToken())`.
  - NOT a bypass site: `post_chat_handler.go:420` `executor.Url` is a **peer**
    API forward (sets `Authorization: request.AuthKey`), not an MLNode — leave it.
  - Riders forced by auth injection:
    - `post_chat_handler.go:611` uses `s.httpClient.Post(...)` (can't set headers)
      → switch to `http.NewRequest + s.httpClient.Do`.
    - `inference_validation.go:920` uses bare `http.Post` (DefaultClient, no
      timeout) → switch to a configured client (same class as #717's open review).

## Sequencing vs other PRs

- Onboarding PR #1296 merges first; this branch sits on it, so Step 2 fixes
  `mlnode_tester.go` (the conflict point) as part of the seam.
- This PR supersedes #717 — comment on #717 to close it once this lands.
- Steps 1–2 are a behavior-preserving seam (could be its own commit/PR);
  Steps 3–5 are the feature on top.
