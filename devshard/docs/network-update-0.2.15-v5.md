# Publication source: devshard deployment v5 host update

> Target: `gonka-ai/gonka-docs`, Network Updates. This file is not itself a
> published network announcement. The release coordinator must replace the two
> UTC placeholders below and publish the resulting entry before running the
> production release gate.

## `<PUBLICATION_DATE_UTC>`

### Required host update: devshard deployment `0.2.15-devshard-v5`

The v5 host deployment adds an actively checked HAProxy versiond pool,
replicated `versiond-router`, per-version readiness, and bounded graceful
shutdown for `versiond`. It also migrates the local HA PostgreSQL deployment
from its v4 anonymous volume to persistent host storage.

**Upgrade deadline (UTC): `<UPGRADE_DEADLINE_UTC>`**

Complete the update before this deadline and before governance enables a
protocol route that requires the v5 per-version router contract. Upgrade one
host at a time and schedule the command outside PoC/cPoC.

Release source: `release/v0.2.15-devshard-v5`

From a stock Gonka checkout:

```bash
git fetch origin \
  refs/tags/release/v0.2.15-devshard-v5:refs/tags/release/v0.2.15-devshard-v5
git switch --detach release/v0.2.15-devshard-v5
cd deploy/join
./upgrade-devshard-v5.sh --preflight-only --strict-capacity
./upgrade-devshard-v5.sh --acknowledge-maintenance
```

The preflight is mandatory and does not pull release application/router images
or recreate, start, stop, or remove deployment services. Its PostgreSQL space
probe may fetch a short-lived helper image. It verifies the immutable updater,
the effective Compose topology, CPU/RAM capacity, public ports, dependencies,
and PostgreSQL copy space. Disk safety is based on the actual PostgreSQL source
size and target filesystem free bytes, not a nominal disk-size threshold.
If the checkout contains tracked local Compose edits, preserve them on a local
branch and merge the release tag; do not reset them. The updater accepts and
reports those files while requiring its own migration code to match the tag.

This is targeted devshard maintenance, not a full node shutdown: chain, signer,
API, and ML containers remain running. The final public nginx-to-HAProxy switch
terminates connections still owned by old nginx. For local HA PostgreSQL, the
old volume is a rollback source only until the new database accepts writes;
after that point, use database backup/recovery rather than switching storage
history backwards.

Supported source topologies are the v0.2.15 standard join stack and the
v0.2.15 base with `0.2.14-devshard-v4` HA overlays: local or managed PostgreSQL,
single or multi edge-api, and complete observability/operator Compose
overrides. The updater preserves the detected edge-api deployment without
changing its containers or images. Pre-v0.2.15 base stacks, pre-v4 devshard, renamed core services,
split Compose projects, Swarm, and Kubernetes are not qualified by this updater.

Full compatibility, local-change, rollback, and recovery details:
<https://github.com/gonka-ai/gonka/blob/release/v0.2.15-devshard-v5/devshard/docs/release-0.2.15-v5.md>
