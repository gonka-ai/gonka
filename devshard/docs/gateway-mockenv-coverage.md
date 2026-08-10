# Gateway Mockenv Coverage

This document maps the gateway request logic to the mock-environment tests in
`devshard/cmd/devshardctl/gateway_mockenv_test.go`.

The mockenv layer is not a full Docker or chain e2e suite. It builds a real
`Gateway`, a real `GatewayStore`, the real gateway HTTP handler stack, and fake
in-memory `devshardRuntime` handlers. This keeps the tests fast while still
exercising gateway routing, auth, access control, limiter, cache, status, and
admin-state behavior through HTTP requests.

## Handler Stack

`buildGatewayHandler` wraps the gateway in this order:

```mermaid
flowchart TD
    Client["HTTP client"]
    Metrics["metrics.Wrap"]
    Disabled["disabledMiddleware"]
    AdminAuth["adminAuthMiddleware"]
    Mux["Gateway.Handler mux"]

    Client --> Metrics
    Metrics --> Disabled
    Disabled --> AdminAuth
    AdminAuth --> Mux

    Disabled -->|"gateway disabled and not admin path"| DisabledResponse["308 JSON replacement response"]
    AdminAuth -->|"admin path and invalid admin key"| Unauthorized["401 invalid admin API key"]
```

Mock coverage:

| Process | Mockenv test |
| --- | --- |
| Public traffic is blocked when the gateway is disabled | `TestGatewayMockEnvDisabledGatewayStillAllowsAdminState` |
| Admin traffic still reaches the gateway when disabled | `TestGatewayMockEnvDisabledGatewayStillAllowsAdminState` |
| Admin paths require the configured admin bearer key | `TestGatewayMockEnvAdminStateRequiresAdminKey` |
| User API keys can satisfy model `api_key` access | `TestGatewayMockEnvAPIKeyModelAccess` |
| Admin key can satisfy model `admin_only` access | `TestGatewayMockEnvAdminOnlyModelAccess` |
| Direct devshard routes enforce model `api_key` access | `TestGatewayMockEnvDirectDevshardEnforcesAPIKeyModelAccess` |
| Direct devshard routes enforce model `admin_only` access | `TestGatewayMockEnvDirectDevshardEnforcesAdminOnlyModelAccess` |

## Route Map

`Gateway.Handler` registers the main gateway surface. Mockenv tests focus on
the request paths where gateway-level decisions happen before a request reaches
a real devshard runtime.

| Route family | Gateway behavior | Mockenv coverage |
| --- | --- | --- |
| `/v1/chat/completions` | Pooled chat: validate, authorize, limit, choose runtime, forward | Covered |
| `/devshard/{id}/v1/chat/completions` | Direct chat: select requested runtime, validate model, limit, forward | Covered |
| `/v1/status` | Single runtime proxies status; multi-runtime returns aggregate gateway status | Multi-runtime covered |
| `/v1/models` | Lists configured gateway models and caps | Indirectly covered through model validation |
| `/v1/admin/state` | Admin-only gateway state view with runtime snapshots and redacted inline keys | Covered |
| `/v1/admin/settings` | Admin-only settings update/read path | Not covered by mockenv yet |
| `/v1/admin/devshards*` | Admin-only add/import/activate/deactivate/settle/participants operations | Not covered by mockenv yet |
| `/v1/admin/escrows` | Admin-only escrow creation path | Not covered by mockenv yet |
| `/v1/admin/suspicious-hosts` | Admin-only suspicious-host list/update path | Not covered by mockenv yet |
| `/v1/finalize`, `/v1/state`, `/v1/debug/*` | Single-runtime or direct-devshard operational/debug paths | Not covered by mockenv yet |
| `/metrics` | Gateway metrics scrape | Not covered by mockenv yet |

## Mock Environment Harness

```mermaid
flowchart TD
    Test["GatewayMockEnv test"]
    RuntimeCfg["gatewayMockRuntime config"]
    Runtime["fake devshardRuntime"]
    Store["GatewayStore in temp gateway.db"]
    Gateway["real Gateway"]
    Handler["real buildGatewayHandler"]
    Request["httptest request"]
    Response["httptest recorder"]

    Test --> RuntimeCfg
    RuntimeCfg --> Runtime
    RuntimeCfg --> Store
    Store --> Gateway
    Runtime --> Gateway
    Gateway --> Handler
    Test --> Request
    Request --> Handler
    Handler --> Response
```

The harness gives each fake runtime:

- an escrow ID
- a model ID
- an active/inactive flag
- optional private-key fields seeded into `GatewayStore`
- an HTTP handler that records call count and returns mock chat JSON or SSE

## Pooled Chat Flow

Endpoint: `POST /v1/chat/completions`

```mermaid
flowchart TD
    Start["POST /v1/chat/completions"]
    Parse["read body, parse model, normalize request, estimate input tokens"]
    ParseError["400 parse or validation error"]
    Model["resolve requested model or default model"]
    Supported["model exists among gateway runtimes"]
    Unsupported["400 unsupported model"]
    Access["model access check: open, api_key, admin_only"]
    AccessDenied["401 access denied"]
    Cache["response cache lookup"]
    CacheHit["serve cached response"]
    Limit["GatewayLimiter acquire for model and input tokens"]
    Limited["429 rate limit exceeded"]
    Select["reserve active runtime for model"]
    SelectFail["4xx/5xx runtime selection failure"]
    Forward["rewrite to /v1/chat/completions and forward to runtime"]
    StoreCache["store cacheable response"]
    Done["client response"]

    Start --> Parse
    Parse -->|"malformed or invalid"| ParseError
    Parse --> Model
    Model --> Supported
    Supported -->|"no"| Unsupported
    Supported -->|"yes"| Access
    Access -->|"denied"| AccessDenied
    Access -->|"allowed"| Cache
    Cache -->|"hit"| CacheHit --> Done
    Cache -->|"miss"| Limit
    Limit -->|"rejected"| Limited
    Limit -->|"acquired"| Select
    Select -->|"failed"| SelectFail
    Select -->|"runtime selected"| Forward
    Forward --> StoreCache --> Done
```

Mock coverage:

| Process | Mockenv test |
| --- | --- |
| Routes pooled chat by requested model | `TestGatewayMockEnvPooledChatRoutesByModel` |
| Uses gateway default model when request omits `model` | `TestGatewayMockEnvPooledChatUsesDefaultModel` |
| Does not inject the default model back into the forwarded body | `TestGatewayMockEnvPooledChatUsesDefaultModel` |
| Rejects unsupported pooled model before runtime call | `TestGatewayMockEnvUnsupportedModelRejectedBeforeRuntime` |
| Rejects malformed JSON before runtime call | `TestGatewayMockEnvMalformedJSONRejectedBeforeRuntime` |
| Enforces `api_key` model access | `TestGatewayMockEnvAPIKeyModelAccess` |
| Enforces `admin_only` model access | `TestGatewayMockEnvAdminOnlyModelAccess` |
| Excludes inactive runtimes from pooled routing | `TestGatewayMockEnvInactiveRuntimeExcludedFromPooledChat` |
| Applies gateway concurrency limiter before runtime call | `TestGatewayMockEnvConcurrencyLimitRejectsBeforeRuntime` |
| Passes OpenAI-style SSE streaming responses through | `TestGatewayMockEnvStreamingChatPassthrough` |
| Cache miss and cache store on successful pooled responses | `TestGatewayMockEnvPooledChatCacheHitSkipsRuntime` |
| Cache hit response replay for pooled chat | `TestGatewayMockEnvPooledChatCacheHitSkipsRuntime` |
| Runtime selection failure when all runtimes are unavailable | not covered by mockenv yet |
| Participant limiter rejecting all candidate runtimes | not covered by mockenv yet |

## Direct Devshard Flow

Endpoint shape: `/devshard/{id}/{inner_path}`

```mermaid
flowchart TD
    Start["/devshard/{id}/{inner_path}"]
    ParsePath["parse devshard id and inner path"]
    BadPath["404 invalid devshard path"]
    Resident["runtime exists in memory"]
    ReadOnly["inner path is read-only GET or HEAD"]
    Admin["request has admin auth"]
    Metadata["serve cheap inactive metadata for /v1/models or /v1/status"]
    MetadataResponse["metadata-only response"]
    Hydrate["hydrate transient read-only runtime from store"]
    Unknown["404 unknown devshard"]
    Chat["inner path is /v1/chat/completions"]
    Available["runtime accepts new inferences"]
    Conflict["409 runtime unavailable"]
    ParseChat["parse chat reservation"]
    ValidateModel["runtime validates requested model"]
    WrongModel["400 unsupported model"]
    Access["model access check"]
    Limit["GatewayLimiter acquire"]
    Reserve["reserve selected runtime"]
    ForwardChat["forward rewritten chat path"]
    Finalize["inner path is /v1/finalize POST"]
    ForwardOther["forward rewritten inner path"]

    Start --> ParsePath
    ParsePath -->|"invalid"| BadPath
    ParsePath --> Resident
    Resident -->|"no"| ReadOnly
    ReadOnly -->|"yes"| Admin
    Admin -->|"yes"| Hydrate
    Admin -->|"no"| Metadata
    Metadata -->|"handled"| MetadataResponse
    Metadata -->|"not handled"| Unknown
    ReadOnly -->|"no"| Unknown
    Hydrate -->|"not known"| Unknown
    Hydrate -->|"known"| ForwardOther
    Resident -->|"yes"| Chat
    Chat -->|"yes"| Available
    Available -->|"no"| Conflict
    Available -->|"yes"| ParseChat
    ParseChat --> ValidateModel
    ValidateModel -->|"wrong model"| WrongModel
    ValidateModel -->|"ok"| Access
    Access --> Limit
    Limit --> Reserve
    Reserve --> ForwardChat
    Chat -->|"no"| Finalize
    Finalize -->|"yes"| ForwardOther
    Finalize -->|"no"| ForwardOther
```

Mock coverage:

| Process | Mockenv test |
| --- | --- |
| Routes direct chat by `/devshard/{id}` | `TestGatewayMockEnvDirectDevshardRouteByID` |
| Rewrites direct route inner path before forwarding | `TestGatewayMockEnvDirectDevshardRouteByID` |
| Rejects direct chat when request model does not match runtime model | `TestGatewayMockEnvDirectDevshardRejectsWrongModel` |
| Returns 404 for unknown direct devshard chat | `TestGatewayMockEnvUnknownDirectDevshardReturnsNotFound` |
| Direct route access control for `api_key` models | `TestGatewayMockEnvDirectDevshardEnforcesAPIKeyModelAccess` |
| Direct route access control for `admin_only` models | `TestGatewayMockEnvDirectDevshardEnforcesAdminOnlyModelAccess` |
| Direct route limiter rejection | not covered by mockenv yet |
| Direct route cache hit | `TestGatewayMockEnvDirectDevshardCacheHitSkipsRuntime` |
| Runtime unavailable or inactive direct chat conflict | not covered by mockenv yet |
| Direct non-chat pass-through paths | not covered by mockenv yet |
| Direct finalize path and post-finalize deactivation | not covered by mockenv yet |
| Non-resident read-only metadata path | not covered by mockenv yet |
| Non-resident admin read-only hydration path | not covered by mockenv yet |

## Status Flow

Endpoint: `GET /v1/status`

```mermaid
flowchart TD
    Start["GET /v1/status"]
    Refresh["refresh capacity scale"]
    Count["count configured runtimes"]
    Single["single runtime"]
    Multi["multiple runtimes"]
    Proxy["proxy status to the only runtime"]
    Aggregate["return gateway aggregate status"]

    Start --> Refresh --> Count
    Count -->|"1 runtime"| Single --> Proxy
    Count -->|"0 or 2+ runtimes"| Multi --> Aggregate
```

Mock coverage:

| Process | Mockenv test |
| --- | --- |
| Multi-runtime status returns aggregate gateway status | `TestGatewayMockEnvMultiRuntimeStatusIsAggregate` |
| Multi-runtime status does not proxy fake runtime handlers | `TestGatewayMockEnvMultiRuntimeStatusIsAggregate` |
| Single-runtime status proxies runtime handler | not covered by mockenv yet |

## Admin State Flow

Endpoint: `GET /v1/admin/state`

```mermaid
flowchart TD
    Start["GET /v1/admin/state"]
    AdminAuth["adminAuthMiddleware validates admin bearer key"]
    Denied["401 invalid admin API key"]
    StoreAvailable["GatewayStore is available"]
    Store["load GatewayStore state"]
    NoStore["503 store unavailable"]
    EmptyState["empty response with current settings and no devshards"]
    RuntimeSnapshots["snapshot in-memory runtimes"]
    Redact["copy devshard records and clear PrivateKeyHex"]
    Response["return settings, devshards, limiter, capacity"]

    Start --> AdminAuth
    AdminAuth -->|"missing or wrong key"| Denied
    AdminAuth -->|"valid key"| StoreAvailable
    StoreAvailable -->|"no"| NoStore
    StoreAvailable -->|"yes"| Store
    Store -->|"not initialized"| EmptyState
    Store -->|"loaded"| RuntimeSnapshots
    RuntimeSnapshots --> Redact
    Redact --> Response
    EmptyState --> Response
```

Mock coverage:

| Process | Mockenv test |
| --- | --- |
| Requires admin key for `/v1/admin/state` | `TestGatewayMockEnvAdminStateRequiresAdminKey` |
| Valid admin key can read state | `TestGatewayMockEnvAdminStateRequiresAdminKey` |
| Admin state still works while gateway disabled | `TestGatewayMockEnvDisabledGatewayStillAllowsAdminState` |
| Admin state does not expose inline private key material | `TestGatewayMockEnvAdminStateDoesNotExposePrivateKey` |
| Admin state omits the `private_key` JSON field | `TestGatewayMockEnvAdminStateDoesNotExposePrivateKey` |
| Store unavailable response | not covered by mockenv yet |
| Empty or uninitialized store response | not covered by mockenv yet |

## Models Flow

Endpoint: `GET /v1/models`

```mermaid
flowchart TD
    Start["GET /v1/models"]
    Method["allow GET or HEAD only"]
    Snapshot["snapshot runtime order and default model"]
    Models["return model list with output token caps"]

    Start --> Method --> Snapshot --> Models
```

Mock coverage:

| Process | Mockenv test |
| --- | --- |
| Pooled chat indirectly depends on model IDs for validation | `TestGatewayMockEnvPooledChatRoutesByModel`, `TestGatewayMockEnvUnsupportedModelRejectedBeforeRuntime` |
| Direct `/v1/models` response shape | not covered by mockenv yet |
| Method rejection for `/v1/models` | not covered by mockenv yet |

## Current Mockenv Coverage Summary

| Gateway area | Covered by mockenv | Current tests |
| --- | --- | --- |
| Handler stack: disabled gateway | Yes | `TestGatewayMockEnvDisabledGatewayStillAllowsAdminState` |
| Handler stack: admin auth | Yes | `TestGatewayMockEnvAdminStateRequiresAdminKey` |
| Pooled chat routing | Yes | `TestGatewayMockEnvPooledChatRoutesByModel` |
| Pooled default model | Yes | `TestGatewayMockEnvPooledChatUsesDefaultModel` |
| Pooled model access modes | Yes | `TestGatewayMockEnvAPIKeyModelAccess`, `TestGatewayMockEnvAdminOnlyModelAccess` |
| Pooled validation before runtime | Yes | `TestGatewayMockEnvUnsupportedModelRejectedBeforeRuntime`, `TestGatewayMockEnvMalformedJSONRejectedBeforeRuntime` |
| Pooled inactive runtime exclusion | Yes | `TestGatewayMockEnvInactiveRuntimeExcludedFromPooledChat` |
| Pooled gateway limiter | Yes | `TestGatewayMockEnvConcurrencyLimitRejectsBeforeRuntime` |
| Pooled streaming pass-through | Yes | `TestGatewayMockEnvStreamingChatPassthrough` |
| Direct devshard routing | Yes | `TestGatewayMockEnvDirectDevshardRouteByID` |
| Direct devshard model validation | Yes | `TestGatewayMockEnvDirectDevshardRejectsWrongModel` |
| Direct devshard model access modes | Yes | `TestGatewayMockEnvDirectDevshardEnforcesAPIKeyModelAccess`, `TestGatewayMockEnvDirectDevshardEnforcesAdminOnlyModelAccess` |
| Direct unknown devshard | Yes | `TestGatewayMockEnvUnknownDirectDevshardReturnsNotFound` |
| Multi-runtime status aggregation | Yes | `TestGatewayMockEnvMultiRuntimeStatusIsAggregate` |
| Admin state auth and redaction | Yes | `TestGatewayMockEnvAdminStateRequiresAdminKey`, `TestGatewayMockEnvAdminStateDoesNotExposePrivateKey` |
| Response cache hit path | Yes | `TestGatewayMockEnvPooledChatCacheHitSkipsRuntime`, `TestGatewayMockEnvDirectDevshardCacheHitSkipsRuntime` |
| Single-runtime `/v1/status` proxy mode | No | Not covered by mockenv yet |
| Direct non-chat pass-through and finalize | No | Not covered by mockenv yet |
| Non-resident read-only metadata and admin hydration | No | Not covered by mockenv yet |
| Runtime selection failure when no runtime accepts new inference | No | Not covered by mockenv yet |
| Participant limiter all-host rejection | No | Not covered by mockenv yet |
