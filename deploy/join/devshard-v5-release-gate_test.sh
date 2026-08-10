#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT
deadline=2026-08-20T12:00:00Z

fail() {
    echo "devshard-v5-release-gate_test: $*" >&2
    exit 1
}

sed \
    -e 's/<PUBLICATION_DATE_UTC>/August 18, 2026/g' \
    -e "s/<UPGRADE_DEADLINE_UTC>/$deadline/g" \
    "$script_dir/../../devshard/docs/network-update-0.2.15-v5.md" \
    >"$tmpdir/published.md"

DEVSHARD_V5_ALLOW_UNRELEASED_SOURCE=true \
DEVSHARD_V5_RELEASE_GATE_ALLOW_UNTAGGED=true \
    "$script_dir/devshard-v5-release-gate.sh" \
    --network-update-file "$tmpdir/published.md" \
    --deadline-utc "$deadline" >"$tmpdir/stdout" || fail \
    "complete publication contract was rejected"

grep -vF "$deadline" "$tmpdir/published.md" >"$tmpdir/no-deadline.md"
if DEVSHARD_V5_ALLOW_UNRELEASED_SOURCE=true \
    DEVSHARD_V5_RELEASE_GATE_ALLOW_UNTAGGED=true \
    "$script_dir/devshard-v5-release-gate.sh" \
    --network-update-file "$tmpdir/no-deadline.md" \
    --deadline-utc "$deadline" >/dev/null 2>"$tmpdir/stderr"; then
    fail "publication without the announced deadline was accepted"
fi

cat >"$tmpdir/published.html" <<EOF
<!doctype html>
<html><body>
<p><span>release/v0.2.15-</span><span>devshard-v5</span></p>
<p>Upgrade deadline: <strong>$deadline</strong></p>
<pre><code><span>./upgrade-devshard-v5.sh</span> <span>--preflight-only</span> <span>--strict-capacity</span></code></pre>
<pre><code><span>./upgrade-devshard-v5.sh</span> <span>--acknowledge-maintenance</span></code></pre>
</body></html>
EOF
DEVSHARD_V5_ALLOW_UNRELEASED_SOURCE=true \
DEVSHARD_V5_RELEASE_GATE_ALLOW_UNTAGGED=true \
    "$script_dir/devshard-v5-release-gate.sh" \
    --network-update-file "$tmpdir/published.html" \
    --deadline-utc "$deadline" >/dev/null || fail \
    "rendered HTML publication contract was rejected"

echo "devshard-v5-release-gate_test: ok"
