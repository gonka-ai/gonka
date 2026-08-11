#!/bin/sh

set -eu

root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
cache_bin="$root/router-runtime/catalog-cache"
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

cat > "$tmpdir/names" <<'EOF'
v9+hotfix
v10;candidate
v11:canary
EOF

"$cache_bin" write "$tmpdir/names" "$tmpdir/catalog.json" 42
[ "$(stat -c '%a' "$tmpdir/catalog.json")" = 600 ]
[ "$(jq -r '.schema, .initialized, .revision' "$tmpdir/catalog.json")" = "$(printf '2\ntrue\n42')" ]
"$cache_bin" read "$tmpdir/catalog.json" 60 > "$tmpdir/actual"
LC_ALL=C sort "$tmpdir/names" > "$tmpdir/expected"
cmp "$tmpdir/expected" "$tmpdir/actual"

printf '%s\n%s\n' duplicate duplicate > "$tmpdir/duplicates"
if "$cache_bin" write "$tmpdir/duplicates" "$tmpdir/duplicate.json" 43 2>/dev/null; then
    echo "catalog-cache accepted a duplicate version" >&2
    exit 1
fi

jq '.fetched_at_unix = 0' "$tmpdir/catalog.json" > "$tmpdir/stale.json"
status=0
"$cache_bin" read "$tmpdir/stale.json" 1 >/dev/null 2>&1 || status=$?
[ "$status" -eq 2 ] || {
    echo "catalog-cache did not report a stale snapshot with exit status 2" >&2
    exit 1
}

jq '.fetched_at_unix = 4102444800' "$tmpdir/catalog.json" > "$tmpdir/future.json"
if "$cache_bin" read "$tmpdir/future.json" 60 >/dev/null 2>&1; then
    echo "catalog-cache accepted a snapshot from the future" >&2
    exit 1
fi

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

for invalid in \
    '{"schema":2,"initialized":false,"revision":42,"fetched_at_unix":0,"versions":[]}' \
    '{"schema":2,"initialized":true,"revision":-1,"fetched_at_unix":0,"versions":[]}' \
    '{"schema":2,"initialized":true,"revision":1.5,"fetched_at_unix":0,"versions":[]}'
do
    printf '%s\n' "$invalid" > "$tmpdir/invalid-v2.json"
    if "$cache_bin" read "$tmpdir/invalid-v2.json" 60 >/dev/null 2>&1; then
        echo "catalog-cache accepted invalid schema 2 metadata" >&2
        exit 1
    fi
done

echo "catalog-cache_test: ok"
