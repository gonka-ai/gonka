#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
network_update_url=
network_update_file=
deadline_utc=

fail() {
    echo "devshard-v5-release-gate: $*" >&2
    exit 1
}

usage() {
    cat >&2 <<'EOF'
Usage: devshard-v5-release-gate.sh \
  (--network-update-url URL | --network-update-file FILE) \
  --deadline-utc YYYY-MM-DDTHH:MM:SSZ

Production releases must use the public gonka.ai Network Updates URL. The file
form exists for deterministic contract tests.
EOF
}

while [[ $# -gt 0 ]]; do
    case $1 in
        --network-update-url)
            [[ $# -ge 2 ]] || fail "--network-update-url requires a value"
            network_update_url=$2
            shift 2
            ;;
        --network-update-file)
            [[ $# -ge 2 ]] || fail "--network-update-file requires a value"
            network_update_file=$2
            shift 2
            ;;
        --deadline-utc)
            [[ $# -ge 2 ]] || fail "--deadline-utc requires a value"
            deadline_utc=$2
            shift 2
            ;;
        -h | --help)
            usage
            exit 0
            ;;
        *)
            usage
            fail "unknown argument: $1"
            ;;
    esac
done

if [[ -n $network_update_url && -n $network_update_file ]] || \
    [[ -z $network_update_url && -z $network_update_file ]]; then
    fail "select exactly one Network Updates source"
fi
[[ $deadline_utc =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] || \
    fail "--deadline-utc must use UTC RFC3339 format"
date -u -d "$deadline_utc" '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null | \
    grep -Fxq "$deadline_utc" || fail "invalid UTC deadline: $deadline_utc"

# shellcheck source=deploy/join/devshard-v5-release-contract.sh
source "$script_dir/devshard-v5-release-contract.sh"
devshard_v5_load_release_contract "$script_dir/devshard-v5-release.env"
devshard_v5_verify_release_source "$script_dir"

case ${DEVSHARD_V5_RELEASE_GATE_ALLOW_UNTAGGED:-false} in
    true | 1 | yes) ;;
    false | 0 | no)
        repo_root=$(git -C "$script_dir" rev-parse --show-toplevel)
        head_commit=$(git -C "$repo_root" rev-parse HEAD)
        [[ $head_commit == "$DEVSHARD_V5_RELEASE_COMMIT" ]] || fail \
            "release gate must run at $DEVSHARD_V5_RELEASE_GIT_TAG, not $head_commit"
        ;;
    *) fail "DEVSHARD_V5_RELEASE_GATE_ALLOW_UNTAGGED must be true or false" ;;
esac

document=$(mktemp)
trap 'rm -f "$document"' EXIT
if [[ -n $network_update_url ]]; then
    [[ $network_update_url == https://gonka.ai/docs/network-updates/* || \
        $network_update_url == https://gonka.ai/docs/network-updates/ ]] || \
        fail "production Network Updates URL must be under https://gonka.ai/docs/network-updates/"
    curl -fsSL --max-time 30 "$network_update_url" >"$document" || \
        fail "cannot read published Network Updates entry"
else
    [[ -f $network_update_file ]] || fail \
        "Network Updates document not found: $network_update_file"
    cp -- "$network_update_file" "$document"
fi

for required in \
    "$DEVSHARD_V5_RELEASE_GIT_TAG" \
    "$deadline_utc" \
    './upgrade-devshard-v5.sh --preflight-only --strict-capacity' \
    './upgrade-devshard-v5.sh --acknowledge-maintenance'; do
    grep -Fq -- "$required" "$document" || fail \
        "Network Updates entry is missing required release contract: $required"
done
if grep -Eq '<(PUBLICATION_DATE_UTC|UPGRADE_DEADLINE_UTC)>' "$document"; then
    fail "Network Updates entry still contains publication placeholders"
fi

echo "Devshard v5 release gate passed: source=$DEVSHARD_V5_RELEASE_COMMIT deadline=$deadline_utc"
