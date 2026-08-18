#!/bin/sh

set -eu

state=${GONKA_UPGRADE_BARRIER_STATE:-/etc/gonka-upgrade-barrier}
[ -f "$state" ] || exit 0

{
    IFS= read -r env_name
    IFS= read -r hosts
    IFS= read -r renderer
} <"$state"

case "$env_name:${renderer##*/}" in
    VERSIOND_HOSTS:40-render-versiond-upstream.sh | \
        EDGE_API_HOSTS:40-render-edge-api-upstream.sh)
        ;;
    *)
        echo "gonka upgrade barrier: invalid state" >&2
        exit 1
        ;;
esac
[ -n "$hosts" ] || {
    echo "gonka upgrade barrier: empty upstream" >&2
    exit 1
}
[ -x "$renderer" ] || {
    echo "gonka upgrade barrier: renderer is not executable: $renderer" >&2
    exit 1
}

export "$env_name=$hosts"
"$renderer"
