#!/usr/bin/env bash

# Sourced by update-devshard.sh for the standalone --check-storage mode.
# Its reference connection is supplied by the operator, never inferred from
# the replica being checked. No Compose model or deployment services are needed.

check_versiond_storage() (
    set -Eeuo pipefail
    local storage_docker=$1 storage_script_dir=$2 storage_config_env=$3 reference_env=$4
    local name tool lock_dir key reference_identity proof state id running
    local checked index snapshot generation nonce request response observed current
    local -a containers reference_keys reference_args ids proofs generations
    shift 4
    containers=("$@")
    ((${#containers[@]} > 0)) || containers=(versiond)
    [[ -f $reference_env && -r $reference_env ]] || fail "cannot read reference settings: $reference_env"
    [[ $reference_env == */* ]] || reference_env=./$reference_env
    for tool in "$storage_docker" jq psql timeout flock; do
        command -v "$tool" >/dev/null 2>&1 || fail "$tool is required for --check-storage"
    done
    for name in "${containers[@]}"; do
        [[ $name =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ ]] || fail "invalid container name: $name"
    done
    "$storage_docker" info >/dev/null 2>&1 || fail "cannot reach the Docker daemon"

    # Use the same local lock as the updater. Cross-host checks must still run
    # sequentially: PostgreSQL has a single challenge field, not a nonce log.
    # shellcheck source=deploy/join/deployment-lock.sh
    source "$storage_script_dir/deployment-lock.sh"
    lock_dir=$(cd -- "$(dirname -- "$storage_config_env")" && pwd -P)
    gonka_acquire_deployment_lock "$lock_dir" || exit 1

    # Isolate libpq from the caller's PGHOST/PGSERVICE/PGOPTIONS, and from any
    # replica configuration. The reference file uses config.env shell syntax.
    # Only these settings are passed to psql, including TLS paths on this host.
    reference_keys=(PGHOST PGPORT PGDATABASE PGUSER PGPASSWORD PGSSLMODE
        PGSSLROOTCERT PGSSLCERT PGSSLKEY PGTARGETSESSIONATTRS PGCONNECT_TIMEOUT)
    for key in ${!PG@}; do unset "$key"; done
    # shellcheck disable=SC1090
    source "$reference_env"
    for key in PGHOST PGDATABASE PGUSER; do
        [[ -n ${!key:-} ]] || fail "$reference_env must set $key explicitly"
    done
    for key in ${!PG@}; do
        [[ " ${reference_keys[*]} " == *" $key "* || -z ${!key:-} ]] || \
            fail "$reference_env sets unsupported $key; use explicit connection settings"
    done
    reference_args=()
    for key in "${reference_keys[@]}"; do
        [[ -z ${!key:-} ]] || reference_args+=("$key=${!key}")
    done
    reference_psql() {
        timeout 60 env -i PATH="$PATH" PGCONNECT_TIMEOUT=5 \
            "${reference_args[@]}" PGOPTIONS='-c statement_timeout=10000' \
            psql -X -w -qAt -v ON_ERROR_STOP=1 -c "$1"
    }
    reference_identity=$(reference_psql \
        'SELECT identity::text FROM devshard_storage_identity WHERE singleton AND NOT pg_is_in_recovery()') || \
        fail "cannot read the reference PostgreSQL database; check --reference-env and connectivity"
    [[ -n $reference_identity && $reference_identity != *$'\n'* ]] || \
        fail "reference PostgreSQL is a standby or has no storage identity"

    read_proof() {
        local proof
        proof=$("$storage_docker" exec "$1" /bin/busybox wget -qO- -T 5 \
            http://127.0.0.1:8080/internal/storage-identity) || \
            fail "$2: storage proof unavailable; wait for stable v5 HA processes and retry"
        jq -e '
            (.identity | type == "string" and length > 0) and
            (.snapshot | type == "string" and length > 0) and
            (.targets | type == "array" and length > 0) and
            (.children == (.targets | length)) and
            all(.targets[];
                (.generation | type == "string" and length > 0) and
                (.version | type == "string" and length > 0)) and
            (([.targets[].generation] | unique | length) == (.targets | length))
        ' <<<"$proof" >/dev/null || fail "$2: incomplete or invalid storage proof"
        [[ $(jq -r .identity <<<"$proof") == "$reference_identity" ]] || \
            fail "$2: PostgreSQL identity differs from the reference database"
        printf '%s\n' "$proof"
    }

    ids=()
    proofs=()
    # Capture every container before writing. Docker IDs and snapshots prevent
    # a replacement or binary swap from silently changing the checked processes.
    for name in "${containers[@]}"; do
        state=$("$storage_docker" inspect --type container --format '{{.Id}} {{.State.Running}}' "$name") || \
            fail "$name: cannot inspect container"
        read -r id running <<<"$state"
        [[ -n $id && $running == true ]] || fail "$name: container is not running"
        "$storage_docker" exec "$id" /bin/busybox wget -qO- -T 5 \
            http://127.0.0.1:8080/readyz >/dev/null || fail "$name: versiond is not ready"
        ids+=("$id")
        proof=$(read_proof "$id" "$name")
        proofs+=("$proof")
    done

    checked=0
    for index in "${!ids[@]}"; do
        name=${containers[index]}
        id=${ids[index]}
        proof=${proofs[index]}
        snapshot=$(jq -r .snapshot <<<"$proof")
        mapfile -t generations < <(jq -r '.targets[].generation' <<<"$proof")
        for generation in "${generations[@]}"; do
            nonce=$(cat /proc/sys/kernel/random/uuid)
            request=$(jq -cn --arg n "$nonce" --arg s "$snapshot" --arg g "$generation" \
                '{operation:"write", nonce:$n, snapshot:$s, generation:$g}')
            response=$("$storage_docker" exec "$id" /bin/busybox wget -qO- -T 5 \
                --header 'Content-Type: application/json' --post-data "$request" \
                http://127.0.0.1:8080/internal/storage-challenge) || \
                fail "$name (generation $generation): cannot write storage challenge; retry after processes stabilize"
            jq -e --arg id "$reference_identity" --arg s "$snapshot" --arg g "$generation" \
                '.identity == $id and .snapshot == $s and .generation == $g and .found == true' \
                <<<"$response" >/dev/null || fail "$name (generation $generation): invalid challenge response"
            observed=$(reference_psql \
                "SELECT identity::text || '|' || COALESCE(challenge::text, '') FROM devshard_storage_identity WHERE singleton AND NOT pg_is_in_recovery()") || \
                fail "$name (generation $generation): cannot read challenge from reference PostgreSQL"
            [[ $observed == "$reference_identity|$nonce" ]] || \
                fail "$name (generation $generation): reference PostgreSQL did not receive its control value; check for a different database, a clone, or a concurrent storage check"
            ((checked += 1))
        done
    done
    for index in "${!ids[@]}"; do
        current=$(read_proof "${ids[index]}" "${containers[index]}")
        [[ $(jq -r .snapshot <<<"$current") == $(jq -r .snapshot <<<"${proofs[index]}") ]] || \
            fail "${containers[index]}: processes changed during verification; run the check again"
    done
    echo "Storage check passed: $checked HA process(es) in ${#ids[@]} container(s) use the reference PostgreSQL database."
)
