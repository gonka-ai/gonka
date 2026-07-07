# Reference — nginx upstream switch

The `proxy` container forwards `/v1/chat/completions` to the gateway. The zero-downtime switch repoints that target from the old gateway to the new one and reloads, without cutting in-flight streams.

## Why in-flight streams survive

`nginx -s reload` is graceful:

1. `nginx -t` validates the new config.
2. `nginx -s reload` starts **new** workers with the new config and tells the **old** workers to stop accepting connections.
3. Old workers keep running until their in-flight requests finish (including long SSE streams), then exit.

New requests go to the new target; existing requests drain on the old workers. Nothing is cut mid-stream — which is why the switch uses reload, never a proxy-container restart.

## What the switch changes

Both the manual `sed` (README step 5) and the script replace the upstream host `<old>:PORT` → `<new>:PORT` in the nginx config (inside the `proxy` container), then run `nginx -t && nginx -s reload`. One `sed` handles **both** config styles:

```nginx
location /devshard-gateway/ { proxy_pass http://devshard-gateway:8080/; }   # direct pass — host swapped
upstream pool { server devshard-gateway:8080; }                              # named upstream — host swapped
# proxy_pass http://pool;  — references the block by name, left untouched (correct)
```

In the config these are `nginx.old_upstream`, `nginx.new_upstream`, `nginx.upstream_port`, `nginx.config_path`, `nginx.proxy_container`.

## Pool (2+ gateways)

A named upstream with multiple `server` lines. Rolling update: mark one `down` (or comment it), reload, drain+update that instance, restore it, reload; repeat. nginx load-balances across whoever is up, so capacity never drops to zero.

```nginx
upstream devshard_gateway {
    server gw-a:8080;
    server gw-b:8080;   # comment out (or `down`) while updating gw-b, then restore
}
```

## Finding the live config

Do not assume the path. The effective config may be `/etc/nginx/nginx.conf`, a file under `conf.d/`, or an included fragment:

```bash
docker exec proxy sh -lc 'nginx -T 2>&1 | grep -n "devshard\|v1/chat\|proxy_pass\|upstream"'
```

Set `nginx.config_path`, `nginx.proxy_container`, `nginx.old_upstream`, `nginx.new_upstream`, and `nginx.upstream_port` in the config to match what you find.

The gateway's compose (`deploy/join/docker-compose.devshard-gateway.yml`) registers it under two network aliases — `${DEVSHARD_INSTANCE_NAME:-devshard-gateway}` and `devshard-pool` — so the proxy could reach it by either. The proxy's own config is not in this repo, so `nginx.old_upstream` (default `devshard-gateway`) is a guess: confirm the real upstream host with `nginx -T` before you switch.

## Gotchas

- **Verify through the public URL, not `127.0.0.1`.** Public verification runs only if `nginx.public_base_url` is set; a loopback check can pass while the public route is broken.
- **Host+port replacement.** The switch rewrites `http://<host>:PORT` and `server <host>:PORT;`. A `proxy_pass` that points at an upstream *name*, a variable, a map, or a non-configured port won't match — set `nginx.old_upstream` / `nginx.new_upstream` / `nginx.upstream_port`, or switch by hand.
- **Always `nginx -t` before reload.** A reload with a broken config keeps the old workers serving but blocks the switch. The switch gates the reload on `nginx -t` and backs up the config first — restore the backup to revert routing instantly.
