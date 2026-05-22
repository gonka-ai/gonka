# aps-client

A reference client for the optional signed agent request envelope.

It generates a principal key and an agent key, builds an envelope, signs it,
and prints the `X-Agent-Passport` and `X-Agent-Sig` headers along with a curl
command that attaches them.

## Run

```
go run ./examples/aps-client
```

## What it shows

1. A principal key (a Gonka account key) and an agent key (a portable Ed25519
   key) are generated.
2. The principal signs an envelope that binds the agent key to the principal
   address with a model allowlist, an expiry, and a beneficiary. The signature
   uses ADR-036, the Cosmos arbitrary-data signing format that Keplr and the
   chain CLI also produce.
3. The agent signs the specific request with its Ed25519 key.
4. The two headers are printed.

## Notes

The envelope is additive metadata. A real inference request still carries
Gonka's existing developer signature and requester headers. The envelope adds
agent identity, request-layer scope, and beneficiary attribution on top.

In production the principal signature is produced by the developer's real key,
for example through Keplr `signArbitrary`. This demo generates a throwaway
principal key so it runs without a wallet.
