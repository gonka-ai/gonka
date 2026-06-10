# mTLS between DAPI and ML nodes (optional)

Protects PoC traffic in both directions: only the holder of the pinned
certificate can talk to the ML node PoC port (8080) or send callbacks to the
DAPI (9100). No CA, nothing expires — two self-signed certs pinned to each
other.

## Enable

```bash
cd deploy/join
./gen-mlnode-certs.sh
source config.env
docker compose -f docker-compose.yml -f docker-compose.mlnode.yml -f docker-compose.mtls.yml up -d
```

No changes to `config.env` or `node-config.json` needed.

**Disable:** start without `-f docker-compose.mtls.yml`.

## Verify

```bash
# without certs - rejected:
curl -k https://localhost:8080/api/v1/state    # handshake failure
curl -k https://localhost:9100/versions        # handshake failure

# with certs - works:
curl --cacert mtls-certs/mlnode.crt --cert mtls-certs/dapi.crt --key mtls-certs/dapi.key \
     https://localhost:8080/api/v1/state       # {"state":...}

# DAPI picked it up and nodes are healthy:
docker logs api 2>&1 | grep "Mutual TLS"
curl -s localhost:9200/admin/v1/nodes | jq '.[].state.current_status'
```

Wrong/missing cert on either side fails loudly at the handshake
(`bad certificate` in logs) — there is no silent fallback to plain HTTP.

## Remote ML node (separate machine)

```bash
DAPI_SANS="DNS:api,DNS:dapi.example.com" MLNODE_SANS="DNS:inference,DNS:mlnode.example.com" ./gen-mlnode-certs.sh
```

Copy `mtls-certs/` to both machines, set
`export MTLS_POC_CALLBACK_URL=https://dapi.example.com:9100` on the DAPI host,
and use the ML node's public name as `host` in `node-config.json`.

## Rotate certs (only on key compromise / host migration)

```bash
rm -rf mtls-certs && ./gen-mlnode-certs.sh
# restart both stacks with the new certs
```

## How it works

```
DAPI ── https + dapi.crt ──> nginx "inference":8080 ──> mlnode    (PoC control)
mlnode ── https + mlnode.crt ──> DAPI:9100                        (PoC callbacks)
```

The existing nginx in front of the ML node terminates TLS and pins `dapi.crt`.
Callbacks are sent by the mlnode FastAPI app with the client certificate; the
vLLM backends (plain HTTP only) POST to a localhost relay inside the same
container, which forwards to the DAPI over mTLS. The inference port
(5000/5050) is not covered yet — follow-up.
