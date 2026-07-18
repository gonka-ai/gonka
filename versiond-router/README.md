# versiond-router

Nginx sticky reverse-proxy in front of N `versiond` instances.

## Routing model

| Backend | Upstream | When |
| --- | --- | --- |
| `versiond_ha_pool` | All `VERSIOND_HOSTS`, `hash $sticky_key consistent` | Version **not** listed in `VERSIOND_NON_HA_VERSIONS` (default) |
| `versiond_legacy` | Only `VERSIOND_LEGACY_HOST` | Version listed in `VERSIOND_NON_HA_VERSIONS` |

Future versions are HA by default. Only pin known pre-HA (SQLite) path segments
in `VERSIOND_NON_HA_VERSIONS`.

When `VERSIOND_HOSTS` has **more than one** host and the request uses
`versiond_ha_pool`, nginx sets request header **`Devshard-Ha: true`**.
`devshardd` rejects that header unless `DEVSHARD_STORAGE_MODE=postgres` and
`PGHOST` are set (`common/storage/mode.RequireConfiguredForHA`).

## Environment

| Variable | Required | Default | Meaning |
| --- | --- | --- | --- |
| `VERSIOND_HOSTS` | yes | — | Space-separated HA pool hostnames |
| `VERSIOND_PORT` | no | `8080` | Upstream listen port |
| `VERSIOND_LEGACY_HOST` | no | first of `VERSIOND_HOSTS` | Host that owns SQLite data dirs for non-HA versions |
| `VERSIOND_NON_HA_VERSIONS` | no | empty | Whitespace and/or comma-separated version path segments pinned to legacy. Empty = all versions use the HA pool |

Debug response headers: `X-Upstream-Addr`, `X-Versiond-Backend`.

### Failover (HA pool)

On the first upstream connect error / timeout / **502**, nginx retries another
peer in `versiond_ha_pool` (`proxy_next_upstream`; `max_fails=1`). Sticky hash
is unchanged while peers are healthy. **503** is not retried (drain / HA guard
must stay sticky). Mid-stream SSE after headers/receipt is not replayed —
clients reconnect with a new request.

## Local render check

```bash
make test-render

VERSIOND_HOSTS='versiond versiond2' \
VERSIOND_LEGACY_HOST=versiond \
VERSIOND_NON_HA_VERSIONS='v1,v2 v3' \
VERSIOND_ROUTER_TEMPLATE=./nginx.conf.template \
VERSIOND_ROUTER_OUT=/tmp/versiond-router.conf \
./entrypoint.sh
```

## Deploy notes

See `devshard/docs/v4-deploy-test-plan.md` §1.2 (routing) and §3 (HA kill / first-502).
