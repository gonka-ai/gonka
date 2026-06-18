# mTLS between DAPI and ML nodes (optional)

Locks down ML node traffic both ways: only the holder of the pinned cert can
reach the ML node's PoC port (8080) or inference port (5000), or post callbacks
to the DAPI (9100). No CA, nothing expires — just two self-signed certs that
pin each other.

## Enable

```bash
cd deploy/join
./gen-mlnode-certs.sh
source config.env
docker compose -f docker-compose.yml -f docker-compose.mlnode.yml -f docker-compose.mtls.yml up -d
```

Nothing to change in `config.env` or `node-config.json`.
To turn it off, just drop `-f docker-compose.mtls.yml`.

## Verify

```bash
# no cert -> rejected
curl -k https://localhost:8080/api/v1/state    # 400 "No required SSL certificate"
curl -k https://localhost:5050/health          # 400 "No required SSL certificate"
curl -k https://localhost:9100/versions        # TLS handshake aborted

# with the right cert -> works
curl --cacert mtls-certs/mlnode.crt --cert mtls-certs/dapi.crt --key mtls-certs/dapi.key \
     https://localhost:8080/api/v1/state        # {"state":...}

# DAPI picked it up and nodes are healthy
docker logs api 2>&1 | grep "Mutual TLS"
curl -s localhost:9200/admin/v1/nodes | jq '.[].state.current_status'
```

A wrong or missing cert always fails loudly — nginx returns a 400 and the DAPI
aborts the handshake. There's no silent fallback to plain HTTP.

## Remote ML node (separate machine)

Put each machine's public IP in its cert SAN (the SAN must match the address the
other side connects to).

```bash
DAPI_SANS="DNS:api,IP:<DAPI_PUBLIC_IP>" MLNODE_SANS="DNS:inference,IP:<MLNODE_PUBLIC_IP>" ./gen-mlnode-certs.sh
```

Then:
1. Copy `mtls-certs/` to both machines.
2. On the DAPI host: `export MTLS_POC_CALLBACK_URL=https://<DAPI_PUBLIC_IP>:9100` (must be `https`).
3. Use the ML node's public IP as `host` in `node-config.json`.

Hostnames work the same way: use `DNS:<name>` in the SANs and the matching names
in `MTLS_POC_CALLBACK_URL` / `node-config.json`.

## Rotate certs (only on key compromise / host migration)

```bash
rm -rf mtls-certs && ./gen-mlnode-certs.sh
# restart both stacks with the new certs
```

## How it works

```
DAPI ── https + dapi.crt ──> nginx "inference":8080 ──> mlnode    (PoC control)
DAPI ── https + dapi.crt ──> nginx "inference":5000 ──> vLLM      (inference)
mlnode ── https + mlnode.crt ──> DAPI:9100                        (PoC callbacks)
```

The nginx in front of the ML node terminates TLS on both ports and pins
`dapi.crt`. Callbacks go out from the mlnode FastAPI app with the client cert;
the vLLM backends (plain HTTP) POST to a localhost relay in the same container,
which forwards to the DAPI over mTLS. devshardd reaches inference endpoints
with the same client cert, configured via `MLNODE_TLS_*` env vars in the
versiond container.
