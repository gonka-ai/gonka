#!/bin/sh

set -eu

root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
cache_bin="$root/router-runtime/catalog-cache"
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

cat > "$tmpdir/names" <<'EOF'
v9+hotfix
v10-candidate
v11.canary
EOF

"$cache_bin" write "$tmpdir/names" "$tmpdir/catalog.json"
[ "$(stat -c '%a' "$tmpdir/catalog.json")" = 600 ]
[ "$(jq -r '.schema' "$tmpdir/catalog.json")" = 1 ]
"$cache_bin" read "$tmpdir/catalog.json" 60 > "$tmpdir/actual"
LC_ALL=C sort "$tmpdir/names" > "$tmpdir/expected"
cmp "$tmpdir/expected" "$tmpdir/actual"

for invalid in 'v10;candidate' 'v11:canary' 'v12 candidate'; do
    printf '%s\n' "$invalid" > "$tmpdir/invalid-name"
    if "$cache_bin" write "$tmpdir/invalid-name" \
        "$tmpdir/invalid-name.json" 2>/dev/null; then
        echo "catalog-cache accepted unroutable version $invalid" >&2
        exit 1
    fi
done

cat > "$tmpdir/legacy-invalid.json" <<EOF
{"schema":1,"fetched_at_unix":$(date +%s),"versions":["v10;candidate"]}
EOF
if "$cache_bin" read "$tmpdir/legacy-invalid.json" 60 2>/dev/null; then
    echo "catalog-cache accepted an invalid snapshot" >&2
    exit 1
fi

printf '%s\n%s\n' duplicate duplicate > "$tmpdir/duplicates"
if "$cache_bin" write "$tmpdir/duplicates" "$tmpdir/duplicate.json" 2>/dev/null; then
    echo "catalog-cache accepted a duplicate version" >&2
    exit 1
fi

jq '.fetched_at_unix = 0' "$tmpdir/catalog.json" > "$tmpdir/stale.json"
status=0
"$cache_bin" read "$tmpdir/stale.json" 1 > "$tmpdir/stale.names" 2>/dev/null || status=$?
[ "$status" -eq 2 ] || {
    echo "catalog-cache did not report a stale snapshot with exit status 2" >&2
    exit 1
}
cmp "$tmpdir/expected" "$tmpdir/stale.names" || {
    echo "catalog-cache did not return the validated names from a stale snapshot" >&2
    exit 1
}

jq '.fetched_at_unix = 4102444800' "$tmpdir/catalog.json" > "$tmpdir/future.json"
if "$cache_bin" read "$tmpdir/future.json" 60 >/dev/null 2>&1; then
    echo "catalog-cache accepted a snapshot from the future" >&2
    exit 1
fi

for unsafe_timestamp in 1e100 9223372036854775808; do
    printf '{"schema":1,"fetched_at_unix":%s,"versions":["v4"]}\n' \
        "$unsafe_timestamp" > "$tmpdir/unsafe-timestamp.json"
    if "$cache_bin" read "$tmpdir/unsafe-timestamp.json" 60 \
        > /dev/null 2> "$tmpdir/unsafe-timestamp.err"; then
        echo "catalog-cache accepted unsafe timestamp $unsafe_timestamp" >&2
        exit 1
    fi
    grep -q '^catalog-cache: invalid snapshot ' "$tmpdir/unsafe-timestamp.err" || {
        echo "catalog-cache passed unsafe timestamp $unsafe_timestamp to shell arithmetic" >&2
        exit 1
    }
done

printf '%s\n' '{"schema":1,"fetched_at_unix":0,"versions":["ok",7]}' \
    > "$tmpdir/non-string.json"
if "$cache_bin" read "$tmpdir/non-string.json" 60 >/dev/null 2>&1; then
    echo "catalog-cache accepted a non-string version" >&2
    exit 1
fi

printf '%s\n' '{not-json' > "$tmpdir/corrupt.json"
if "$cache_bin" read "$tmpdir/corrupt.json" 60 >/dev/null 2>&1; then
    echo "catalog-cache accepted corrupt JSON" >&2
    exit 1
fi

now=$(date +%s)
printf '{"schema":1,"fetched_at_unix":%s,"versions":["legacy"]}\n' "$now" \
    > "$tmpdir/legacy.json"
[ "$("$cache_bin" read "$tmpdir/legacy.json" 60)" = legacy ] || {
    echo "catalog-cache rejected a valid legacy snapshot" >&2
    exit 1
}

status_bin="$root/router-runtime/catalog-status"
now=$(date +%s)
jq -n --argjson observed "$now" \
    '{schema: 1, state: "ready", detail: "current", observed_at_unix: $observed}' \
    > "$tmpdir/status.json"
ROUTING_CATALOG_STATUS_MAX_AGE_SECONDS=10 \
    "$status_bin" --state "$tmpdir/status.json" | jq -e '.state == "ready"' >/dev/null
jq --argjson observed "$((now - 20))" '.observed_at_unix = $observed' \
    "$tmpdir/status.json" > "$tmpdir/status-stale.json"
ROUTING_CATALOG_STATUS_MAX_AGE_SECONDS=10 \
    "$status_bin" --state "$tmpdir/status-stale.json" \
    | jq -e '.state == "stale" and (.detail | contains("stale"))' >/dev/null

echo "catalog-cache_test: ok"
