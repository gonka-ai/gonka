#!/bin/sh
# Project versiond's running children onto the pre-rendered non-HA peer-RPC
# twins. A single-replica proxy starts with an empty version map: nginx sends
# JSON straight to versiond, and nothing lists protocol versions for HAProxy.
# Without a map entry every peer RPC stays on the coarse pool, and that pool's
# /readyz?peer-rpc=1 check is host-wide, so one HTTP/1.1 child takes it down.
#
# watch asks versiond GET /healthz, gives each running child a dynamic slot,
# and enables that slot's _rpc server. The twin's own check (peer-rpc-ok for
# that version) decides HTTP/2 versus the bounded fallback. A failed fetch
# leaves the last assignment in place. Versions already declared statically
# keep the twins rendered for them and are not projected again.

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
runtime_contract=${ROUTER_RUNTIME_VERSION_CONTRACT:-}
if [ -z "$runtime_contract" ]; then
    if [ -r "$script_dir/../router-runtime/version-contract" ]; then
        runtime_contract=$script_dir/../router-runtime/version-contract
    else
        runtime_contract=/usr/local/lib/router-runtime/version-contract
    fi
fi
# shellcheck disable=SC1090,SC1091
. "$runtime_contract"

versiond_url=${NONHA_PEER_RPC_VERSIOND_URL:-http://versiond:8080/healthz}
version_map=${NONHA_PEER_RPC_VERSION_MAP:-}
slot_map=${NONHA_PEER_RPC_SLOT_MAP:-}
runtime_socket=${NONHA_PEER_RPC_RUNTIME_SOCKET:-/var/run/haproxy/reconciler.sock}
backend_prefix=${NONHA_PEER_RPC_BACKEND_PREFIX:-versiond_routers_dynamic_}
server_name=${NONHA_PEER_RPC_SERVER:-versiond}
poll_seconds=${NONHA_PEER_RPC_POLL_SECONDS:-1}
fetch_timeout=${NONHA_PEER_RPC_FETCH_TIMEOUT_SECONDS:-2}
runtime_timeout=${NONHA_PEER_RPC_RUNTIME_TIMEOUT_SECONDS:-2}
static_map=${NONHA_PEER_RPC_STATIC_MAP:-$version_map}

log() {
    printf '%s\n' "proxy-router: $*" >&2
}

# show map lines are "<id> <key> <value>". Force string compares: a version
# such as "10" must not become an awk number.
map_pairs() {
    awk 'NF >= 3 { print $2, $3 }'
}

same_word() {
    awk -v want="$1" -v field="$2" '
        BEGIN { field = field + 0 }
        ($(field) "") == (want "") { found = 1 }
        END { exit !found }
    '
}

numeric_slots() {
    awk -F_ '{ print $NF "\t" $0 }' | sort -n -k1,1 | cut -f2-
}

# plan_projection reads a /healthz body and show-map dumps, then prints
# "release <backend> <version>", "assign <backend> <version> <encoded>", and
# "reset <backend>". Releases come first.
plan_projection() {
    health_file=$1
    version_dump=$2
    slot_dump=$3
    static_file=$4
    names=$5

    jq -r '.[] | select(.status == "running") | .name' "$health_file" \
        | LC_ALL=C sort -u > "$names.raw" || return 1
    : > "$names"
    while IFS= read -r version; do
        [ -n "$version" ] || continue
        router_version_is_valid "$version" || continue
        if [ -f "$static_file" ] && grep -Fxq "$version" "$static_file"; then
            continue
        fi
        printf '%s\n' "$version" >> "$names"
    done < "$names.raw"

    : > "$names.current"
    if [ -s "$version_dump" ]; then
        map_pairs < "$version_dump" | awk -v prefix="$backend_prefix" '
            index($2, prefix) == 1 { print $1, $2 }
        ' > "$names.current"
    fi
    : > "$names.slots"
    if [ -s "$slot_dump" ]; then
        map_pairs < "$slot_dump" | awk -v prefix="$backend_prefix" '
            index($1, prefix) == 1 { print $1, $2 }
        ' > "$names.slots"
    fi

    : > "$names.kept"
    while read -r version backend; do
        [ -n "$version" ] || continue
        grep -Fxq "$version" "$names" || continue
        encoded=$(router_urlencode "$version")
        assigned=$(awk -v b="$backend" '($1 "") == (b "") { print $2; exit }' "$names.slots")
        [ "$assigned" = "$encoded" ] || continue
        printf '%s %s\n' "$version" "$backend" >> "$names.kept"
    done < "$names.current"

    : > "$names.plan"
    while read -r version backend; do
        [ -n "$version" ] || continue
        same_word "$version" 1 < "$names.kept" && continue
        printf 'release %s %s\n' "$backend" "$version" >> "$names.plan"
    done < "$names.current"

    # A slot that already holds this version's encoding, but is not in the
    # version map, is a crashed assign. Reuse it instead of leaking it.
    : > "$names.reuse"
    while IFS= read -r version; do
        [ -n "$version" ] || continue
        same_word "$version" 1 < "$names.kept" && continue
        encoded=$(router_urlencode "$version")
        backend=$(awk -v e="$encoded" '($2 "") == (e "") { print $1; exit }' "$names.slots")
        [ -n "$backend" ] || continue
        same_word "$backend" 2 < "$names.kept" && continue
        printf '%s %s\n' "$version" "$backend" >> "$names.reuse"
    done < "$names"

    : > "$names.free"
    awk '($2 "") == "__unassigned__" { print $1 }' "$names.slots" | numeric_slots > "$names.free"
    next_free=1
    while IFS= read -r version; do
        [ -n "$version" ] || continue
        same_word "$version" 1 < "$names.kept" && continue
        backend=$(awk -v v="$version" '($1 "") == (v "") { print $2; exit }' "$names.reuse")
        encoded=$(router_urlencode "$version")
        if [ -z "$backend" ]; then
            backend=$(sed -n "${next_free}p" "$names.free")
            if [ -z "$backend" ]; then
                log "non-HA peer RPC has no free dynamic slot for '$version'"
                continue
            fi
            next_free=$((next_free + 1))
        fi
        printf 'assign %s %s %s\n' "$backend" "$version" "$encoded" >> "$names.plan"
    done < "$names"

    while read -r backend encoded; do
        [ -n "$backend" ] || continue
        [ "$encoded" = "__unassigned__" ] && continue
        same_word "$backend" 2 < "$names.kept" && continue
        same_word "$backend" 2 < "$names.reuse" && continue
        same_word "$backend" 2 < "$names.plan" && continue
        printf 'reset %s\n' "$backend" >> "$names.plan"
    done < "$names.slots"

    cat "$names.plan"
}

runtime_key() {
    printf '%s' "$1" | sed 's/;/\\;/g'
}

runtime() {
    response=$(printf '%s;show info\n' "$1" | \
        socat -t "$runtime_timeout" -T "$runtime_timeout" \
            - "UNIX-CONNECT:$runtime_socket") || return 1
    printf '%s\n' "$response" | awk '
        /^Name: HAProxy$/ { answered = 1; exit }
        { lines[++count] = $0 }
        END {
            if (!answered) exit 1
            while (count > 0 && lines[count] == "") count--
            for (i = 1; i <= count; i++) print lines[i]
        }
    '
}

apply_projection() {
    plan=$1
    while read -r action backend version encoded; do
        [ -n "${action:-}" ] || continue
        case "$action" in
            release)
                runtime "del map $(runtime_key "$version_map") $(runtime_key "$version")" >/dev/null 2>&1 || true
                runtime "disable server ${backend}_rpc/${server_name}" >/dev/null 2>&1 || true
                runtime "set map $(runtime_key "$slot_map") $(runtime_key "$backend") __unassigned__" >/dev/null || return 1
                log "non-HA peer RPC released $version"
                ;;
            assign)
                # Enable before publishing the version. A failed enable leaves
                # the slot holding the encoding, and the next pass reuses it.
                runtime "set map $(runtime_key "$slot_map") $(runtime_key "$backend") $(runtime_key "$encoded")" >/dev/null || return 1
                runtime "enable server ${backend}_rpc/${server_name}" >/dev/null || return 1
                runtime "add map $(runtime_key "$version_map") $(runtime_key "$version") $(runtime_key "$backend")" >/dev/null || return 1
                log "non-HA peer RPC projected $version onto ${backend}_rpc"
                ;;
            reset)
                runtime "disable server ${backend}_rpc/${server_name}" >/dev/null 2>&1 || true
                runtime "set map $(runtime_key "$slot_map") $(runtime_key "$backend") __unassigned__" >/dev/null || return 1
                log "non-HA peer RPC cleared unclaimed slot $backend"
                ;;
            *)
                log "non-HA peer RPC plan action '$action' is unknown"
                return 1
                ;;
        esac
    done < "$plan"
}

project_once() {
    work=$(mktemp -d)
    status=0
    if ! curl --fail --silent --show-error \
        --proto '=http' --connect-timeout "$fetch_timeout" --max-time "$fetch_timeout" \
        --output "$work/health.json" "$versiond_url"; then
        status=1
    elif ! runtime "show map $(runtime_key "$version_map")" > "$work/versions.dump"; then
        status=1
    elif ! runtime "show map $(runtime_key "$slot_map")" > "$work/slots.dump"; then
        status=1
    else
        awk '{ print $1 }' "$static_map" > "$work/static.names"
        if ! plan_projection "$work/health.json" "$work/versions.dump" "$work/slots.dump" \
            "$work/static.names" "$work/names" > "$work/plan"; then
            status=1
        elif ! apply_projection "$work/plan"; then
            status=1
        fi
    fi
    rm -rf "$work"
    return "$status"
}

usage() {
    echo "usage: nonha-peer-rpc.sh watch | nonha-peer-rpc.sh plan <health.json> <version-dump> <slot-dump> <static-names>" >&2
    exit 2
}

case "${1:-}" in
    watch)
        [ -n "$version_map" ] && [ -n "$slot_map" ] || usage
        for value in "$poll_seconds" "$fetch_timeout" "$runtime_timeout"; do
            case "$value" in
                '' | *[!0-9]* | 0)
                    log "non-HA peer RPC poll timing must be positive integers"
                    exit 1
                    ;;
            esac
        done
        # Snapshot declared versions once. Later Runtime API additions must
        # not become "static" if HAProxy writes them back into the map file,
        # or the next pass would release the twin it just published.
        static_names=$(mktemp)
        trap 'rm -f "$static_names"' EXIT
        if [ -n "$static_map" ] && [ -f "$static_map" ]; then
            awk '{ print $1 }' "$static_map" > "$static_names"
        else
            : > "$static_names"
        fi
        static_map=$static_names
        projection_down=0
        log "projecting non-HA peer RPC from $versiond_url"
        while :; do
            if project_once; then
                if [ "$projection_down" -eq 1 ]; then
                    log "non-HA peer RPC projection recovered"
                    projection_down=0
                fi
            elif [ "$projection_down" -eq 0 ]; then
                log "non-HA peer RPC projection failed; keeping the last assignment"
                projection_down=1
            fi
            sleep "$poll_seconds"
        done
        ;;
    plan)
        [ "$#" -eq 5 ] || usage
        work=$(mktemp -d)
        trap 'rm -rf "$work"' EXIT
        plan_projection "$2" "$3" "$4" "$5" "$work/names"
        ;;
    *)
        usage
        ;;
esac
