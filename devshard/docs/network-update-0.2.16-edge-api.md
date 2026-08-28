# Edge API HA update 0.2.16

This update moves the existing edge-api pool behind the public HAProxy and its
two policy workers. It updates the edge-api and ingress images while leaving
PostgreSQL, versiond, and the versiond-router fleet unchanged.

The host must first complete the 0.2.15 devshard update. The updater verifies
its completion marker and the active public proxy before changing containers.
Published release images are pinned by digest. Mutable `:0.2.16` tags are
accepted only when a release engineer explicitly enables staging mode.

## Update

Use the same ordered Compose files and project identity as the running host.
The updater can recover them from container labels for a standard deployment:

```bash
cd deploy/join
./upgrade-edge-api-0.2.16.sh --preflight-only
./upgrade-edge-api-0.2.16.sh --acknowledge-maintenance
```

For a deployment started with explicit Compose options, pass the complete
ordered file list and project identity to both commands:

```bash
./upgrade-edge-api-0.2.16.sh \
  --compose-project-name gonka \
  --compose-project-directory "$PWD" \
  --compose-file docker-compose.yml \
  --compose-file docker-compose.versiond.yml \
  --compose-file docker-compose.edge-api-multi.yml \
  --preflight-only
```

Repeat the command with `--acknowledge-maintenance` after the preflight passes.
The acknowledgement is required because the public proxy is recreated and
existing ingress connections can close.

## Update sequence

On a multi-replica host, the updater removes one edge-api replica at a time
from the existing nginx router, replaces it, verifies its health and a real
`/v1/versions` request, and restores it before continuing. A persistent nginx
startup hook keeps that exclusion active if the router restarts mid-update.

After every edge-api replica is ready, the updater rolls both policy workers
and the public HAProxy through the existing ingress transaction. The ingress
transaction verifies the versiond-router fleet but does not update it. The old
edge-api nginx router is removed only after the new public path is healthy.

A single-replica edge-api deployment has no reserve during its replacement, so
edge-api routes are briefly unavailable. Other public routes remain available.

## Recovery

The operation is serialized by the deployment lock. A failed edge-api
replacement restores the captured image and the full old nginx upstream. The
ingress transaction has its own rollback journal. After fixing the reported
cause, run the same update command again; completed healthy replicas are
detected and skipped.

Success is recorded in
`deploy/join/.gonka-edge-api-0.2.16-upgrade-complete` (or next to the selected
`config.env`).
