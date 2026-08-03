# Proposal: Public API Contract and Regression Detector

## Goal

Make every change to Gonka's consumer-facing public API detectable and
deliberate. A pull request that removes an endpoint, renames a response field,
or changes a JSON type must remain blocked until the change is recorded in a
committed contract and reviewed through the normal pull request process.

## Problem

Gonka exposes its public API through the participant proxy. The surface has
four parts: `/v1/*` and `/v2/*` served by dapi and edge-api, `/chain-api/*`
served by grpc-gateway REST, `/chain-rpc/*` served by CometBFT JSON-RPC, and
`/devshard/*` served by versiond. Dashboards and explorers (the gonkascan
explorer, https://github.com/gonka-ai/gonkascan, is the reference consumer),
bridges, wallets, and participant tooling all depend on this surface.

Nothing protects it today:

- An OpenAPI spec exists only for the 22 query routes (20 GET, 2 POST)
  served by edge-api (`common/queryapi/openapi.yaml`). The roughly 20 dapi-native
  public routes (identity, PoC proofs, stats, bridge, v2
  participants/accounts) have no spec.
- The chain's embedded OpenAPI (`inference-chain/docs/static/openapi.yml`)
  has drifted from the current protobuf definitions in
  `inference-chain/proto`: 36 existing routes are not documented, 11
  documented routes no longer exist, and 9 paths reference the removed
  NetworkNodeService.
- No test calls the 107 custom grpc-gateway REST routes generated from
  `inference-chain/proto` over HTTP, so a route removal or a serialization
  change there ships without any test failing.
- Partial guards already exist for the edge surface
  (`common/queryapi/tests/routes_contract_test.go` and related tests).
  They cover route lists and some top-level keys, not full schemas, and
  nothing binds the dapi-native or chain REST surfaces the same way.
- Testermint verifies behavior (epochs, inference, governance), not schemas.
  Its Gson clients silently tolerate unknown and missing JSON fields.

As a result, renaming a response field or changing a numeric field such as
`weight` from integer to string breaks every consumer with zero CI signal.

## Proposal

Two pieces. A committed `api-contract/` directory is the reviewable registry
and compatibility baseline for the public surface; the authoritative code
remains in each owning service. An API Regression Detector blocks a pull
request whenever code diverges from the registry. A committed contract change
is reported as additive or breaking for normal pull request review.

### Contract directory

```
api-contract/
  dapi/openapi.yaml        # dapi-native public routes (hand-authored)
  edge/openapi.yaml        # generated copy of common/queryapi/openapi.yaml
  chain/openapi.yml        # regenerated from inference-chain protos
  chain/descriptors.binpb  # protobuf descriptor image (buf breaking input)
  chain/cometbft-rpc.yaml  # CometBFT JSON-RPC method + shape contract
  chain/cosmos-rest.yaml   # Cosmos REST routes in the consumer floor
  devshard/openapi.yaml    # chat/completions + stats/shards routes
  surface.yaml             # generated path + method + backend + exposure map
  detector/                # Go wrapper over oasdiff + buf breaking
```

Generated files use a deterministic canonical form, so diffs stay small and
reviewable. `edge/openapi.yaml` is a generated copy of
`common/queryapi/openapi.yaml`; CI fails if the two diverge. `surface.yaml`
replaces separate route inventories and proxy-exposure files. It records the
external method and path, owning backend, aliases, and whether each operation
is published, blocked, disabled, or method-split. The detector configuration
lists the generated artifact paths explicitly, including both
`api-contract/chain/openapi.yml` and
`inference-chain/docs/static/openapi.yml`. Hand-authored contracts are
validated but are not part of regeneration-drift comparison.

`chain/cometbft-rpc.yaml` and `chain/cosmos-rest.yaml` are OpenAPI 3 documents,
so oasdiff validates and compares them like the dapi and devshard contracts.
The detector compares `surface.yaml` separately: publishing a new operation or
alias is additive, while removing, blocking, disabling, method-restricting, or
incompatibly rerouting a published operation is breaking.

`chain/openapi.yml` must list only routes actually mounted over REST. The
current generation config (`inference-chain/proto/buf.gen.swagger.yaml`) sets
`generate_unbound_methods=true`, which documents RPC methods that have no
HTTP binding. Contract generation disables that option and compares the
resulting operations with the registered `*.pb.gw.go` handlers. Focused Go
tests pin protobuf JSON encoding for int64, bytes, Any, BLS, pricing, and PoC
responses.

The field rule is strict, because loose optionality is how renames slip
through as one optional field removed plus one optional field added:

- A field the handler always emits is marked required.
- A genuinely conditional field is optional only when its absence condition
  is documented and covered by a fixture. Distinct successful body shapes use
  `oneOf`.
- Configuration-dependent availability is represented by response statuses,
  such as 200 with storage and 503 without it, not alternate success schemas.
- Owned object schemas set `additionalProperties: false` recursively.
  Intentional protobuf passthrough fields are explicitly marked opaque.

### Compatibility policy

Additive changes, meaning new endpoints and new response fields, are allowed
but must be declared by updating `api-contract/` in the same PR. New fields
still follow the field rule above. The closed schemas and the additive policy
work at different points: unknown-key failure applies to our own validation
tests, which is what forces the spec update into the same PR; the base-vs-head
compatibility check then classifies that declared new field as additive and
allowed. Consumers are expected to tolerate additive fields.

Every declared contract change goes through the repository's normal pull
request review. The compatibility report classifies it as additive or
breaking so reviewers see the impact. A change is breaking when it removes an
endpoint or method, removes or renames a response field, changes a JSON type
or format (including int -> string), narrows an enum, adds a required request
field, changes a status code or content type incompatibly, or changes a
protobuf field number, type, or cardinality.

### Detection layers

Detection runs at three layers, from cheapest to most realistic.

1. Two static checks that must not be conflated. The drift check regenerates
   every generated artifact at the PR head and requires it to equal the
   committed version, so a code change cannot alter the surface without
   touching `api-contract/`. The compatibility check diffs the committed
   contract at the merge base against the committed contract at the PR head;
   comparing regenerated head artifacts against committed head artifacts
   would self-cancel whenever a PR updates its own baseline. Both wrap
   oasdiff for OpenAPI and buf breaking in the repository's existing `FILE`
   mode for protobuf descriptors. Tool and generator versions are pinned.
   The detector commits an oasdiff severity configuration matching the policy
   above and produces one report format. Self-test fixtures seed one break per
   regression class (removed route, renamed field, int -> string, narrowed
   enum, new required request field, changed status code, changed content
   type, changed proto field number, published route blocked in
   `surface.yaml`) and assert the detector flags each one.
2. Owner-local Go `httptest` tests run the real routers with stub
   dependencies. The shared edge query routes stay in `common/queryapi/tests`;
   dapi-native routes are tested in
   `decentralized-api/internal/server/public`; devshard session routes are
   tested with their router in `devshard/server` and `devshard/transport`;
   shard stats are tested in `devshard/cmd/devshardd/session`. Route-parity
   tests compare each owner's method and path pairs with its spec. A coverage
   meta-test requires every operation to have a validated representative
   response for each declared profile, with a 2xx fixture only when the
   operation defines a success response. Validation covers nested types,
   required fields, unknown keys, status codes, content types, and
   deprecation headers. The loop is plain `go test` with no Docker.
3. One Testermint class, `PublicApiContractTests`, proves only what the real
   proxy can prove: backend selection, blocked and disabled routes, the
   `/api/v1`, `/api/v2`, and `/v1/devshard/` aliases, the `/chain-api` and
   `/chain-rpc` prefixes, the `x-cosmos-block-height` header, and the devshard
   public routes. It smoke-validates the gonkascan floor using addresses,
   epochs, and heights resolved from the running chain. Exact serialization
   stays in deterministic Go tests. This class runs only in the existing
   Testermint integration CI; it is not a dependency of the required
   `API contract` check.

### CI gate

One required status check, `API contract`, owns the whole gate.

- Authors run `make api-contract` locally to regenerate the configured
  generated artifacts and print the additive/breaking report.
- Both local verification and CI run `make api-contract-check`. It regenerates
  the configured generated artifacts into a clean temporary directory,
  compares each one with its committed path, validates all hand-authored
  contracts, and runs detector fixtures, owner-local Go tests, and chain
  registration and serialization tests. It requires no Docker or live local
  network.
- A correctly committed contract change passes CI and exposes its
  compatibility report in the check summary. Reviewers handle it through the
  repository's normal pull request review.

The whole gate is the required `API contract` check plus normal pull request
review. No additional review machinery is layered on top.

### Scope

In scope is the consumer-facing API published through the participant proxy
for dapi, edge-api, inference-chain, and devshard, plus the proxy's own
`/health` endpoint. The proxy publishes both API versions (`API_VERSIONS`
defaults to `v1 v2` in `proxy/entrypoint.sh`), so `/v2/*` and its `/api/v2/*`
alias are covered the same way as `/v1/*`.

For devshard the contract covers only the endpoints external clients call:
`/devshard/{version}/sessions/{id}/chat/completions` and the versionless shard
stats routes (`/devshard/stats/shards` and
`/devshard/stats/shards/{escrow_id}`). These form one version-independent
consumer facade that every supported devshard binary must preserve.
Everything else under `/devshard/` is excluded: the node-to-node protocol
routes (gossip, challenge-receipt, payloads, verify-timeout) because their
only consumers are other Gonka nodes and the versioned
`/devshard/{version}/` prefix isolates protocol changes, and the remaining
operational routes (session diffs, mempool, signatures, metrics, healthz) as
node diagnostics, not a consumer API.

Coverage depth differs by surface:

- dapi, edge-api, and all 107 custom grpc-gateway REST routes are fully
  contracted (OpenAPI plus protobuf descriptors for the chain).
- Cosmos REST and CometBFT JSON-RPC initially cover only routes used by the
  reference gonkascan explorer. The route list comes from
  `gonka-ai/gonkascan/backend/src/backend/client.py`. The contract records the
  exact gonkascan commit used to produce the list. The initial floor contains
  the `/chain-rpc/` methods `status`, `block`, `block_results`, and `genesis`,
  plus Cosmos routes for bank, staking, slashing, authz, gov, and tx. Other
  consumers add routes by PR.

`/api/v1/*` aliases `/v1/*`, `/api/v2/*` aliases `/v2/*`, and the legacy
`/v1/devshard/*` prefix is rewritten by the proxy to `/devshard/v1/*`. Each
alias gets one status/body/schema equivalence check instead of a separate
contract; backend-specific deprecation headers may differ. The legacy
devshard alias requires the trailing slash. Exact `/v1/devshard` continues to
return dapi's 410 response.

Default `deploy/join` disables `/chain-api` and `/chain-rpc`
(`DISABLE_CHAIN_API` / `DISABLE_CHAIN_RPC` default to true). Testermint and
surface-exposure checks run under a contract profile that enables those
prefixes, matching explorer-serving deployments.

Out of scope for now are `/chain-grpc/*` (the protobuf descriptor check
already guards gRPC at the source), the admin, ML callback, NodeManager, and
NATS listeners (not reachable through the proxy), observability UIs, and
runtime comparison against the previous release image (the dual-endpoint
harness in `common/queryapi/tests/compatibility/` can host that later).

## Implementation

Three sequential commits, each independently green.

1. Create `api-contract/` with the dapi OpenAPI document, the generated edge
   copy, `surface.yaml`, the detector wrapper with pinned tools and self-test
   fixtures, owner-local Go contract tests with route parity and coverage,
   `make api-contract`, and the required `API contract` workflow result.
2. Add the chain and devshard contracts: the protobuf descriptor image with
   its buf breaking baseline, the regenerated chain OpenAPI covering all 107
   custom routes with unbound methods excluded and obsolete and orphan paths
   removed, the CometBFT and Cosmos REST floor contracts, the devshard
   OpenAPI document with owner-local route-parity and schema tests, chain
   REST registration and serialization tests, and generation-drift checks in
   the proto CI workflow.
3. Add `PublicApiContractTests` to Testermint under a profile that enables
   `/chain-api` and `/chain-rpc`, with fixture resolvers and surface-exposure
   assertions and gonkascan-floor smoke validation. Add it to the existing
   Testermint integration CI, not the required `API contract` check. Make the
   fast `API contract` context required in the main branch ruleset.

The work touches `api-contract/` (new), `proposals/public-api-contract/`,
`decentralized-api/internal/server/public/contract_*_test.go`,
`common/queryapi/tests/`, `devshard/server/`, `devshard/transport/`,
`devshard/cmd/devshardd/session/` (contract tests),
`common/queryapi/openapi.yaml`
(complete the RawProtoJson placeholder schemas), a bound-methods-only
swagger generation config next to `inference-chain/proto/buf.gen.swagger.yaml`,
`inference-chain/docs/static/openapi.yml` (regenerate),
`testermint/src/test/kotlin/PublicApiContractTests.kt`,
`scripts/validate-edge-api.sh`, the `API contract` workflow in
`.github/workflows/`, and the main branch ruleset entry for the required
`API contract` check.
