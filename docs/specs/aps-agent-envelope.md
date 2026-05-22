# Agent Request Envelope

Status: proposal, reference implementation in `decentralized-api/internal/agentenvelope`.

## Overview

The agent request envelope is an optional, signed metadata layer on inference
completion requests. When a request carries the `X-Agent-Passport` and
`X-Agent-Sig` headers, a middleware on `/v1/chat/completions` and
`/v1/completions` verifies that:

- the envelope was signed by a Gonka principal (the developer key), via ADR-036
- the principal public key embedded in the envelope derives to the stated
  principal address, so no chain query is needed for signature verification
- the agent signed this specific request, binding the chain id, method, path,
  envelope, and body
- the envelope is within its validity window and its configured maximum TTL
- the requested model is within the delegated scope

On success the verified context is attached for the handler, which confirms
the envelope principal matches the request requester (directly or through an
`x/authz` grant) and emits a structured attribution event after the inference
is submitted to chain.

The envelope answers three questions the existing identity model cannot:
which agent key made the request, what request-layer scope it was authorized
for, and which beneficiary the work was attributed to.

## What this does not change

The envelope is additive. It does not replace or modify the developer
signature on the AuthKey, the Transfer Agent signature, the Executor
signature, AuthKey replay protection, timestamp validation, `x/authz`
semantics, escrow, settlement, or `MsgFinishInference`. When the
`X-Agent-Passport` header is absent, request behavior is identical to a node
that does not run the middleware.

## Envelope schema

```json
{
  "v": "aps-agent-envelope-v1",
  "audience": "gonka",
  "chain_id": "<gonka chain id>",
  "agent_id": "urn:aps:agent:<id>",
  "agent_pubkey": { "type": "ed25519", "public_key": "<base64, 32 bytes>" },
  "principal_address": "<gonka bech32 address>",
  "principal_pubkey": { "type": "secp256k1", "public_key": "<base64, 33 bytes>" },
  "scope": {
    "allowed_models": ["Qwen/QwQ-32B"],
    "expires_at": "<RFC3339>",
    "not_before": "<RFC3339, optional>"
  },
  "beneficiary": "urn:customer:<id>",
  "issued_at": "<RFC3339>",
  "principal_sig": "<base64 ADR-036 signature>"
}
```

`allowed_models` uses field-presence semantics: omitted means all models are
allowed, present and empty means no model is allowed, present and non-empty
restricts to the listed models.

A v1 envelope carries only the fields above. The verifier parses it strictly:
an envelope with an unknown field, a duplicate member name, or trailing
content after the envelope object is rejected as malformed. Extension fields
are a v2 concern.

## Signing model

The principal signature covers the envelope with `principal_sig` set to the
empty string, canonicalized with a JCS canonicalizer (RFC 8785, scoped to the
string, object, and array shapes this schema uses), wrapped in an ADR-036
`MsgSignData` document, and signed with the principal's secp256k1 key.

The agent signature is `Ed25519.Sign` over a domain-separated, length-prefixed
payload:

```
"APS-AGENT-SIG-V1\n" ||
u32_be(len(chain_id))     || chain_id ||
u32_be(len(method))       || method   ||
u32_be(len(request_uri)) || request_uri ||
u32_be(len(envelope_jcs)) || envelope_jcs ||
u32_be(len(body))         || body
```

request_uri is the full request target, the path with any query string, so
the signature binds the exact endpoint. The length prefixes make field
boundaries unambiguous. The construction follows the TLS 1.3 transcript
pattern (RFC 8446 section 4.4.1). The agent key signs the payload directly
with no SHA-256 prehash.

## Verification flow

The middleware runs, in order: header presence and base64 decode, strict
envelope parse (unknown fields, duplicate member names, and trailing content
rejected), version, audience, chain id, TTL bound, time window, principal
pubkey type and decode, address derivation check, agent pubkey type and
decode, beneficiary validation, ADR-036 principal signature, bounded body
read, agent signature, model scope. Any failure stops the flow and returns the mapped HTTP
status. The handler then performs the principal-to-requester binding.

This binding, and the attribution that follows it, happen on the client-facing
transfer-request path only. A Transfer Agent forwards work to an executor with
a fixed header set that does not include the APS headers, so executor-hop
requests carry no envelope and do not participate in APS v1. The verifier also
rejects an envelope whose JSON contains duplicate member names, since a
duplicate name is ambiguous at a signature boundary.

HTTP status mapping:

- 400: malformed structure (bad base64, bad JSON, a header pair with one
  header missing)
- 401: a wrong credential (bad signature, wrong chain id or audience, unknown
  version, address mismatch, wrong pubkey type, invalid beneficiary)
- 403: a valid credential that is not permitted (expired, not yet valid, TTL
  too long, model out of scope, principal mismatch)
- 413: request body over the configured maximum
- 500: a chain query failure during authz grantee resolution

## Attribution event

After a verified, request-bound inference request is submitted to chain
(MsgStartInference), a structured inference-start attribution event is emitted
through an asynchronous bounded-queue emitter. This is start-of-request
attribution, not execution or settlement attribution. The event carries the
agent id, principal address, beneficiary, model, inference id, and content
hashes. The emitter drops on a full queue and counts the drops, so logging
backpressure never stalls a response. v1 attribution is observability
data, not an on-chain record.

## Configuration

The `agent_envelope` config block: `enabled` (default false), `chain_id`,
`max_ttl_seconds` (default 3600), `max_body_size` (default 10 MiB), and
`attribution_queue_cap` (default 1024). The layer is off unless `enabled` is
set.

`chain_id` must match this network's chain id; an envelope carrying a
different chain id is rejected. If the layer is enabled with an empty
`chain_id` the node refuses to start: a verifier with no chain id would reject
every envelope, and a misconfigured security and attribution feature should
fail loudly at startup rather than run silently degraded. `max_ttl_seconds`,
`max_body_size`, and `attribution_queue_cap` are each bounded to a sane range;
a value outside its range falls back to the default, since each has a safe
default whereas `chain_id` has none. Deriving `chain_id` from the chain node
automatically is deferred future work.

## Backward compatibility

With the layer disabled, or with no `X-Agent-Passport` header, request
behavior is byte-identical to baseline. The middleware is a passthrough.

## Security limitations in v1

- No revocation. A bounded maximum TTL limits the lifetime of a stolen
  envelope; there is no revocation list.
- The envelope is not bound to a specific host. A passport plus signature pair
  is an agent capability, not a single-host token. The agent signature is per
  request, so a captured request cannot be replayed against a different body,
  and Gonka's existing AuthKey replay protection still applies.
- Multi-signature principal keys are rejected. v1 supports single-key
  secp256k1 principals only.
- The beneficiary is a principal-attested opaque string. The network does not
  verify it as a real-world identity.

## Future work

These are out of scope for this proposal and would be discussed separately:
on-chain attribution by extending `MsgFinishInference` with optional agent and
beneficiary fields, scoped delegation chains, devshard integration, and a
revocation mechanism.
