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

"$cache_bin" write "$tmpdir/names" "$tmpdir/catalog.json"
[ "$(stat -c '%a' "$tmpdir/catalog.json")" = 600 ]
"$cache_bin" read "$tmpdir/catalog.json" 60 > "$tmpdir/actual"
LC_ALL=C sort "$tmpdir/names" > "$tmpdir/expected"
cmp "$tmpdir/expected" "$tmpdir/actual"

printf '%s\n%s\n' duplicate duplicate > "$tmpdir/duplicates"
if "$cache_bin" write "$tmpdir/duplicates" "$tmpdir/duplicate.json" 2>/dev/null; then
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

echo "catalog-cache_test: ok"
