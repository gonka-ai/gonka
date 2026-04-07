# Issue: Safely reject or redirect inference during PoC (cPoC-aware devshards)

## Summary

Devshards must be **aware of cPoC events** (tracked from **mainnet** or authoritative source) and expose a **safe** way to **reject** inference that must not run under current PoC rules, or **redirect** it to hosts that have **`POC_SLOT=true`**.

## Motivation

- During PoC windows, executing inference on the wrong participants may violate network policy or waste capacity.
- **Subnet random participant selection** should be updated so the group **always retains some fraction** of **`POC_SLOT=true`** participants, making redirection or scheduling **feasible**.

## Scope (high level)

1. **Ingest cPoC / mainnet signals** into devshard logic (config + event stream or polling).
2. **Gate or reroute** `StartInference` / executor assignment: reject with clear reason vs **redirect** to a PoC-eligible host.
3. **Adjust selection / slot assignment** so PoC-capable slots exist with **configured minimum percentage**.

## Status

Open — protocol.
