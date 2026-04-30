# v0.2.12 Pre-Upgrade Model Cleanup

Run on each DAPI host before v0.2.12 lands.

## Issue

v0.2.12 removes every governance model that is not in the post-upgrade approved list. On mainnet only the previously-enforced model and Kimi remain.

Each DAPI persists its MLNode configs locally. On startup it validates every configured model against the on-chain governance list. If config has at least one not supported model, the whole node is rejected and the host goes offline. v0.2.11 masked the problem by trimming the runtime view down to the enforced model, so `/admin/v1/nodes` looked clean even when the persisted config still listed extras. v0.2.12 stops trimming, and the persisted config is what loads.

## Solution

For each node in `/admin/v1/config` with extra models, PUT a cleaned config to `/admin/v1/nodes/<id>`. The change is persisted within 60s. The kept model's args, hardware, and ports are preserved exactly. Nodes that don't list the enforced model are skipped and need manual fixing.

## Usage

Paste this into the host's shell. Defaults to commit. Set `APPLY=dry` (or any value other than `--apply`) to preview without changes.

```bash
ADMIN=${ADMIN:-http://127.0.0.1:9200}
KEEP=${KEEP:-Qwen/Qwen3-235B-A22B-Instruct-2507-FP8}
APPLY=${APPLY:-"--apply"}

curl -sS "$ADMIN/admin/v1/config" | jq -r --arg k "$KEEP" '
  .nodes[] | "\(.id): " + (
    if (.models | has($k) | not) then "skip (\(.models | keys))"
    elif (.models | length) == 1 then "ok"
    else "\(.models | keys) -> [\($k)]" end)'

if [[ "$APPLY" == "--apply" ]]; then
  curl -sS "$ADMIN/admin/v1/config" \
    | jq -c --arg k "$KEEP" \
        '.nodes[] | select((.models | has($k)) and (.models | length > 1)) | .models = {($k): .models[$k]}' \
    | while IFS= read -r p; do
        id=$(jq -r .id <<<"$p")
        curl -sS -f -X PUT -H 'Content-Type: application/json' -d "$p" \
          "$ADMIN/admin/v1/nodes/$id" >/dev/null && echo "$id: updated"
      done
  echo "done; persisted within 60s"
else
  echo "preview only; rerun without APPLY=dry to commit"
fi
```

## Verify

```
curl -sS http://127.0.0.1:9200/admin/v1/config \
  | jq '.nodes[] | {id, models: (.models | keys)}'

Wait 60s after the run before triggering the upgrade so the change has been persisted.
