# Model MLNode addressing + auth as a value object (`mlnode.Endpoint`)

To add BaseURL/FQDN + bearer-token MLNode registration (superseding the stalled
PR #717), we model "how the API node reaches an MLNode" as a value object —
`mlnode.Endpoint`, a Go interface with two mutually-exclusive implementations
(Host-Port, BaseURL) living in a new leaf package `decentralized-api/mlnode` —
rather than adding `BaseURL`/`AuthToken` as loose fields on `InferenceNodeConfig`
and `broker.Node` (the approach written into `proposals/mlnode-token-auth/`).

## Why

The addressing concept was never named: 5 fields duplicated across two structs,
reassembled into URLs in 4 places. #717 added a second addressing mode (baseURL +
token) by extending that scatter — 34 files, and reviewers kept catching missed
propagation sites (e.g. "you missed BaseURL/AuthToken here"). A value object that
owns URL construction, the health-endpoint difference, and the auth token gives
the baseURL-XOR-host rule and the per-mode behavior exactly one home; the two
interface implementations make the illegal "both set" state unrepresentable by
construction.

## Considered options

- **Loose fields on both structs (the proposal).** Smallest conceptual leap, but
  re-spreads the mode logic and the XOR rule across every call site — the exact
  cause of #717's sprawl and its missed-propagation review comments.
- **Single struct with an internal `if baseURL != ""` branch.** Collapses the
  scatter to one place, but leaves illegal states representable (both set) and
  relies on runtime validation to catch them.
- **Interface + two implementations (chosen).** Mode-specific URL/health/auth
  behavior is polymorphic and self-contained; XOR is enforced at construction, so
  illegal states cannot exist.

## Consequences

- New leaf package `mlnode`; `apiconfig.MLNodeURL` moves into it (apiconfig →
  mlnode, one-way, no cycle). `InferenceNodeConfig` and `broker.Node` each expose
  `.Endpoint()`.
- `.Endpoint()` is total (no error), matching the existing infallible URL
  builders; URL *validity* is checked once at the registration/config boundary
  (`ValidateInferenceNodeBasic`, made mode-aware), not at runtime.
- `mlnodeclient.CreateClient(pocUrl, inferenceUrl)` is replaced wholesale by
  `CreateClientForNode(ep mlnode.Endpoint, version)` — all call sites and ~14 test
  sites migrate; there is intentionally no transitional second path.
- Legacy `segment` fields are kept (always empty in practice) and confined to the
  Host-Port variant; dropping them + the SQLite migration is deferred to a
  separate PR to keep this one a behavior-preserving seam.
