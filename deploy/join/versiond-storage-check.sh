#!/usr/bin/env bash

# Sourced by update-devshard.sh for the standalone --check-storage mode.
# Its reference connection is supplied by the operator, never inferred from
# the replica being checked. No Compose model or deployment services are needed.

check_versiond_storage() (
    set -Eeuo pipefail
    local storage_docker=$1 storage_script_dir=$2 storage_config_env=$3 reference_env=$4
    local name tool lock_dir key reference_identity proof state id running project
    local checked index snapshot generation nonce request response observed current
    local storage_image
    local -a containers reference_keys reference_args ids proofs generations
    shift 4
    containers=("$@")
    ((${#containers[@]} > 0)) || containers=(versiond)
    [[ -f $reference_env && -r $reference_env ]] || fail "cannot read reference settings: $reference_env"
    [[ $reference_env == */* ]] || reference_env=./$reference_env
    for tool in "$storage_docker" jq timeout flock sha256sum; do
        command -v "$tool" >/dev/null 2>&1 || fail "$tool is required for --check-storage"
    done
    for name in "${containers[@]}"; do
        [[ $name =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ ]] || fail "invalid container name: $name"
    done
    "$storage_docker" info >/dev/null 2>&1 || fail "cannot reach the Docker daemon"

    # Isolate libpq from the caller's PGHOST/PGSERVICE/PGOPTIONS, and from any
    # replica configuration. The reference file uses config.env shell syntax.
    # Only these settings are passed to the containerized PostgreSQL client.
    reference_keys=(PGHOST PGPORT PGDATABASE PGUSER PGPASSWORD PGSSLMODE
        PGTARGETSESSIONATTRS PGCONNECT_TIMEOUT)
    for key in ${!PG@}; do unset "$key"; done
    # shellcheck disable=SC1090
    source "$reference_env"
    for key in PGHOST PGDATABASE PGUSER; do
        [[ -n ${!key:-} ]] || fail "$reference_env must set $key explicitly"
    done
    for key in ${!PG@}; do
        if [[ $key == PGSSL* && -n ${!key} && \
            ( $key != PGSSLMODE || ${!key} != disable ) ]]; then
            fail "$reference_env sets unsupported TLS setting $key; --check-storage supports PostgreSQL without explicit TLS configuration (PGSSLMODE=disable is allowed)"
        fi
        [[ " ${reference_keys[*]} " == *" $key "* || -z ${!key:-} ]] || \
            fail "$reference_env sets unsupported $key; use explicit connection settings"
    done
    reference_args=()
    for key in "${reference_keys[@]}"; do
        [[ -z ${!key:-} ]] || reference_args+=(--env "$key=${!key}")
    done
    storage_image=${VERSIOND_STORAGE_CHECK_IMAGE:-postgres:16-alpine}
    # Prepare the client before taking the deployment lock so a slow registry
    # cannot block other deployment operations. Pull progress goes to stderr.
    "$storage_docker" image inspect "$storage_image" >/dev/null 2>&1 || \
        timeout --kill-after=5 300 "$storage_docker" pull "$storage_image" >&2 || \
        fail "cannot prepare PostgreSQL client image $storage_image (download failed or timed out)"

    # Use the same local lock as the updater. Cross-host checks must still run
    # sequentially: PostgreSQL has a single challenge field, not a nonce log.
    # shellcheck source=deploy/join/deployment-lock.sh
    source "$storage_script_dir/deployment-lock.sh"
    lock_dir=$(cd -- "$(dirname -- "$storage_config_env")" && pwd -P)
    project=$("$storage_docker" inspect --format '{{index .Config.Labels "com.docker.compose.project"}}' "${containers[0]}") || \
        fail "cannot inspect container ${containers[0]}"
    [[ $project != '<no value>' ]] || project=
    # Unlabelled standalone containers have no Compose project. Give their
    # local checks a stable identity without borrowing an unrelated proxy's.
    project=${project:-versiond-storage-check}
    gonka_acquire_deployment_lock "$lock_dir" "$project" || exit 1

    reference_psql() (
        local client_name
        client_name="gonka-storage-psql-$(cat /proc/sys/kernel/random/uuid)"
        # A killed Docker CLI can leave its container running; remove it on exit.
        trap '"$storage_docker" rm -fv "$client_name" >/dev/null 2>&1 || true' EXIT
        # Keep the CLI in the caller's process group so Ctrl+C reaches it too.
        timeout --foreground 60 "$storage_docker" run --rm --init --name "$client_name" \
            --network host --read-only \
            --env PGCONNECT_TIMEOUT=5 "${reference_args[@]}" \
            --env 'PGOPTIONS=-c statement_timeout=10000' \
            --entrypoint psql "$storage_image" \
            -X -w -qAt -v ON_ERROR_STOP=1 -c "$1"
    )
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
