#!/usr/bin/env bash

# The rendered publish lists exactly the ready members, for an explicit
# endpoint file and for DNS discovery. A host with no ready version is omitted.

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

fail() {
    echo "publish-members_test: $*" >&2
    exit 1
}

cat >"$tmpdir/endpoints.json" <<'EOF'
[
  {"id": "versiond", "host": "versiond", "port": 8080},
  {"id": "versiond-b", "host": "10.20.0.12", "port": 18080},
  {"id": "versiond-c", "host": "10.20.0.13"}
]
EOF

cat >"$tmpdir/ready.json" <<'EOF'
{
  "versiond": ["v2"],
  "versiond-b": ["v4", "v2"]
}
EOF

file_doc=$(
    VERSIOND_POOL_ENDPOINTS_FILE="$tmpdir/endpoints.json" \
    VERSIOND_READY_FILE="$tmpdir/ready.json" \
    VERSIOND_ROUTER_ID=router-a \
    "$script_dir/publish-members"
)

printf '%s' "$file_doc" | jq -e '
  .members == [
    {"id":"versiond","addr":"versiond:8080","ready_versions":["v2"]},
    {"id":"versiond-b","addr":"10.20.0.12:18080","ready_versions":["v2","v4"]}
  ]
' >/dev/null || fail "endpoint file publish was $file_doc"

# v2 is ready on versiond and versiond-b; v4 only on versiond-b; versiond-c is down.
v2=$(printf '%s' "$file_doc" | jq -r '[.members[] | select(.ready_versions | index("v2")) | .id] | sort | join(",")')
v4=$(printf '%s' "$file_doc" | jq -r '[.members[] | select(.ready_versions | index("v4")) | .id] | sort | join(",")')
[ "$v2" = "versiond,versiond-b" ] || fail "v2 ready set was $v2"
[ "$v4" = "versiond-b" ] || fail "v4 ready set was $v4"

cat >"$tmpdir/dns-ready.json" <<'EOF'
{ "versiond-pool": ["v2"] }
EOF

dns_doc=$(
    VERSIOND_POOL_HOST=versiond-pool \
    VERSIOND_PORT=8080 \
    VERSIOND_READY_FILE="$tmpdir/dns-ready.json" \
    VERSIOND_ROUTER_ID=router-a \
    "$script_dir/publish-members"
)
printf '%s' "$dns_doc" | jq -e '
  .members == [
    {"id":"versiond-pool","addr":"versiond-pool:8080","ready_versions":["v2"]}
  ]
' >/dev/null || fail "DNS publish was $dns_doc"

echo "publish-members_test: ok"
