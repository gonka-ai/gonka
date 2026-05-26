# teeverify

Off-chain verification of TEE attestation quotes for the `gonka` encrypted
inference pipeline.

The inference chain only stores a SHA-256 commitment to each quote
(`TeeAttestation.quote_hash`). The actual cryptographic validation that
the quote was produced by a real TEE, running known-good code, and binds
to the declared HPKE public key happens off chain through a `Verifier`
implementation registered with this package.

If you want to know how the whole encrypted-inference pipeline fits
together, start with [`mlnode/packages/api/src/api/encrypted/`](../../mlnode/packages/api/src/api/encrypted/)
and [`x/inference/keeper/tee_attestation.go`](../../inference-chain/x/inference/keeper/tee_attestation.go).

## Shipped verifiers

| Provider id        | Class                  | Status      | Notes                                                                  |
|--------------------|------------------------|-------------|------------------------------------------------------------------------|
| `intel-tdx-lite`   | `IntelTDXLiteVerifier` | Registered  | Offline TDX V4 verify (signature + cert chain + report_data binding).  |
| `intel-tdx`        | `IntelTDXVerifier`     | Skeleton    | Strict variant; PCS-backed TCB/QE/CRL checks. Not yet registered.      |

There is **no `local` verifier**. The previous developer-mode pass-through
was removed because it gave operators a false sense of TEE protection. If
you need to exercise the HPKE round-trip without TDX hardware, run the
dstack simulator on the ml-node side; if you need to test the verifier
layer specifically, mock the `Verifier` interface in your test file.

## Trust model

There are three independent gates a TEE-attested public key must pass
before any client encrypts a request to it. Bypassing any one of them
defeats the others.

| Gate                                | Where                                                    | Who controls it          |
|-------------------------------------|----------------------------------------------------------|--------------------------|
| Provider allowlist                  | `TeeAttestationParams.allowed_providers`                 | chain governance         |
| Measurement allowlist (mandatory)   | `TeeAttestationParams.allowed_measurements`              | chain governance         |
| Quote signature & report_data bind  | this package, via `Verifier.Verify`                      | the dapi binary you ship |

The chain enforces the first two (cheap, deterministic, replayable). This
package enforces the third (expensive, requires Internet for some
providers, requires native libraries for others). Both checks must pass
for a node's pubkey to be trusted.

### Where verification runs

`Verifier.Verify` is invoked at two natural points:

1. **DAPI worker, pre-flight.** Before `poc/tee_attestation_worker.go`
   submits `MsgSubmitTeeAttestation`, it runs the verifier locally. A
   failed verification skips the node so we don't pollute the chain with
   garbage commitments.
2. **End-user client (future).** A client that wants to use encrypted
   inference reads `TeeAttestation` from the chain, fetches the raw quote
   from the host out-of-band (typically as part of the first encrypted
   request), checks `sha256(quote) == quote_hash`, and runs the verifier.
   Only on success does it encrypt the prompt to the on-chain pubkey.

## `intel-tdx-lite` vs `intel-tdx`

Both verify the same TDX V4 quote shape. They differ in what dynamic data
they consult.

| Check                                       | `intel-tdx-lite` | `intel-tdx` (strict, future) |
|---------------------------------------------|------------------|------------------------------|
| ECDSA signature over the quote body         | ✓                | ✓                            |
| PCK cert chain up to embedded Intel Root CA | ✓                | ✓                            |
| `report_data` == `sha256(public_key) \|\| 0` | ✓                | ✓                            |
| TCB Info freshness (TCB downgrade detection)| ✗                | ✓ (Intel PCS)                |
| QE Identity freshness                       | ✗                | ✓ (Intel PCS)                |
| CRL revocation of PCK leaf                  | ✗                | ✓ (Intel PCS)                |
| Network required                            | none             | Intel PCS                    |
| Suitable for                                | MVP / devnet     | mainnet                      |

The intent is to roll `intel-tdx-lite` first, then add `intel-tdx`
without disrupting deployed ml-nodes: both produce the same quote, only
the verifier (and governance allowlist) changes.

## Adding a new provider

1. **Pick the canonical provider string.** Lower-case, dash-separated,
   matches what the ML node advertises in its `/api/v1/encrypted/identity`
   response. Examples already in use: `intel-tdx-lite`, `intel-tdx`,
   `amd-sev-snp`, `nvidia-cc`. Don't invent synonyms for an existing
   provider -- update the existing one instead.

2. **Implement the `Verifier` interface in a new file.** Convention is
   one file per provider, named after the provider:

   ```go
   // teeverify/amd_sev_snp.go
   package teeverify

   const AMDSEVSNPProvider = "amd-sev-snp"

   type AMDSEVSNPVerifier struct {
       // dependencies: cert cache, ...
   }

   func (v AMDSEVSNPVerifier) Provider() string { return AMDSEVSNPProvider }

   func (v AMDSEVSNPVerifier) Verify(ctx context.Context, req VerifyRequest) (*Report, error) {
       // 1. Parse req.Quote in the provider's native format.
       // 2. Verify the signature chain against the provider's root of trust.
       // 3. Confirm the quote binds to req.PublicKey via the provider's
       //    equivalent of TDX report_data (e.g. AMD's REPORT_DATA field).
       // 4. Extract the platform measurement, encode lowercase hex.
       // 5. Return &Report{Measurement: mHex, TcbStatus: ..., QuoteHash: ...}.
   }
   ```

   Key contractual obligations -- if you skip any of these your verifier
   gives a false sense of security:

   - **Verify the signature chain end-to-end**, not just the leaf
     signature. The whole point of TEE attestation is that you trust the
     manufacturer's root, not the prover.
   - **Bind the quote to the public key.** Quote's `report_data` (or the
     equivalent for non-TDX providers) MUST contain a commitment to
     `req.PublicKey`. If you skip this check, an attacker lifts somebody
     else's valid quote and pairs it with their own pubkey. See
     `BindReportData` in `intel_tdx_lite.go` for the canonical 64-byte
     layout: `sha256(publicKey) || 0...0`.
   - **Return an extracted measurement, not the one the prover claimed.**
     The `Report.Measurement` field gates the chain's `AllowedMeasurements`
     check; if you copy a field from the wire payload instead of
     extracting it from the verified quote, the allowlist becomes
     advisory.
   - **Lowercase, hex-encoded measurement, no separators.** Matches the
     chain's canonical form so allowlist comparison is a plain
     `slices.Contains`.

3. **Register it in `default.go`.** Add one line to `NewDefaultRegistry`:

   ```go
   r.Register(AMDSEVSNPVerifier{ /* deps */ })
   ```

   We deliberately don't auto-register from `init()` (see comment on
   `NewDefaultRegistry`).

4. **Update chain params via governance.** Add `"amd-sev-snp"` to
   `TeeAttestationParams.allowed_providers` AND seed
   `allowed_measurements` with the measurement of the audited ML node
   build. Both are mandatory: an empty `allowed_measurements` list
   rejects every attestation, regardless of provider.

5. **Update ML node to emit a real quote.** Today
   `mlnode/packages/api/src/api/crypto/key_provider.py` ships only
   `DstackKeyProvider` (TDX). A new provider needs its own
   `KeyProvider` subclass that pulls the platform-specific quote and
   returns it via `Identity.quote`.

6. **Tests.** Mirror `intel_tdx_lite_test.go`: a happy-path test with a
   fixed test-vector quote shipped by the provider's reference library,
   plus negative tests for tampered bytes, mismatched `report_data`, and
   (where applicable) revoked TCB. Real verifier libraries usually ship
   test vectors -- use them.

## What this package is NOT

- It is **not** the on-chain handler. Chain-side validation lives in
  `inference-chain/x/inference/keeper/msg_server_submit_tee_attestation.go`.
- It is **not** a wire format. The transport between the ML node and the
  dapi worker is fixed by `mlnodeclient/encrypted.go`; this package
  consumes its outputs.
- It is **not** a key store. Keys live on the ML node; this package only
  validates the proof that the ML node holds them.

## Fail-closed behaviour

If a participant configures a node with `provider = "intel-tdx"` but the
binary has no `IntelTDXVerifier` registered (today's state — strict
verifier ships separately), the submission is dropped locally with a
loud log line. The same applies if governance allow-lists a provider id
that the dapi binary does not implement: those attestations are simply
ignored. A half-deployed verifier never silently attests bogus nodes.
