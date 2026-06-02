# Inspecting committed state size (`inferenced state-stats`)

`state-stats` is an offline analysis command that answers two questions about a
node's on-disk state:

1. Which module store consumes the most space?
2. Within the `inference` module, which prefix (feature) consumes the most
   space, and which of those are legacy / cleanup candidates?

It was added for issue #1223 ("clean up the state") so we can drive cleanup
decisions from measured data instead of guessing.

## How it works

The command opens the node's `application.db` directly, loads the latest
committed height (or `--height`), and iterates every committed KV store,
summing key and value bytes. For the `inference` store it additionally
attributes every key to a named prefix using
`x/inference/types.StatePrefixCatalog`, and flags prefixes marked `legacy`.

Because it opens the database exclusively, **the node must be stopped** (or run
it against a restored snapshot copy).

## Usage

```bash
# Stop the node first, then:
inferenced state-stats --home /path/to/node/home

# Inspect a specific committed height:
inferenced state-stats --home /path/to/node/home --height 1234567

# Only the 20 largest inference prefixes:
inferenced state-stats --home /path/to/node/home --top 20

# Only legacy (cleanup-candidate) inference prefixes:
inferenced state-stats --home /path/to/node/home --legacy-only
```

## Output

Two tables:

- **Per-store summary** — every module KV store with key count, key bytes,
  value bytes, and a humanized total, sorted largest first, plus a grand total.
- **Inference prefix breakdown** — every inference prefix with a `LEGACY`
  column (`yes` = cleanup candidate, `?` = key not recognized by the catalog),
  sorted largest first.

A non-empty `<unmatched:0xNN>` row means a key prefix is not yet in the
catalog; add it to `StatePrefixCatalog` so the attribution stays complete.

## Relationship to the v0.2.14 cleanup

The v0.2.14 upgrade handler already removes the known legacy prefixes
(`EpochGroupValidations` aggregate map, `TopMiner`, training state, legacy PoC
v2 prefixes). Run `state-stats --legacy-only` before and after the upgrade on a
snapshot to confirm those prefixes drop to zero, and use the full breakdown to
decide whether any additional large prefixes warrant their own cleanup task.
