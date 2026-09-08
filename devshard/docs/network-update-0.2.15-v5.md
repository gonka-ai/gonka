# Publication source: devshard deployment v5 host update

> Target: `gonka-ai/gonka-docs`, Network Updates. Replace the placeholders and
> the release reference before publishing.

## `<PUBLICATION_DATE_UTC>`

### Host update: devshard deployment `0.2.15-devshard-v5`

The v5 deployment replaces the public nginx proxy with HAProxy in front of two
private nginx policy workers, runs `versiond-router` as a three-slot HAProxy
fleet with per-version readiness checks, gives `versiond` graceful shutdown,
and moves the local HA PostgreSQL cluster to persistent host storage.

Update one host at a time, outside PoC/cPoC. On an HA host the run restarts the
shared PostgreSQL once and closes the connections the old public proxy holds.

From the checkout that runs your node:

```bash
git fetch origin
git checkout <RELEASE_REF>
cd deploy/join
source ./config.env
./update-devshard.sh --check
./update-devshard.sh
```

`--check` leaves services in place but writes PostgreSQL probes and storage
challenges. The second command prints its Compose steps. Failed public-ingress
or versiond replacements restore the saved container configuration and stop;
an interrupted replacement is recovered on the next normal run.

Single-versiond hosts stay single; hosts with the HA overlay keep their configured
replicas. Operator Compose overlays are picked up from the running deployment.
For a planned rollback, select compatible previous images and Compose files,
then follow the release guide. Details, multi-host versiond, and the PostgreSQL rollback boundary:
`devshard/docs/release-0.2.15-v5.md` in the release.
