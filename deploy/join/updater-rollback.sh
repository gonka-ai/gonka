#!/usr/bin/env bash

# Functions sourced by the host updater. Keep one previous specification per
# actual change and one durable pending transaction, shared by all checkouts
# of this deployment. The pending directory is published before any replacement.
# shellcheck disable=SC2154

initialize_rollback() {
    state_dir=${UPDATE_STATE_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/gonka/updater/$GONKA_DEPLOYMENT_KEY}
    state_helper=$script_dir/updater-container-state.py
    [[ -f $state_helper ]] || fail "missing $state_helper"
    if [[ -d $state_dir/pending ]]; then
        [[ $check_only == false && $dry_run == false ]] || fail \
            "an interrupted update has pending recovery in $state_dir; run the updater normally to restore it first"
        echo "Recovering the previous service specifications from an interrupted update"
        restore_pending || fail "recovery is incomplete; records remain in $state_dir/pending"
    fi
    trap rollback_on_exit EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM
}

restore_pending() {
    local file
    [[ -d $state_dir/pending ]] || return 0
    if [[ -f $state_dir/pending/postgres.json ]]; then
        # Resume the frozen migration target, never restore old PGDATA.
        python3 "$state_helper" postgres-recover "$state_dir/pending/postgres.json" || return 1
        python3 "$state_helper" commit "$state_dir/pending"
        return $?
    fi
    # Create/start all old public dependencies before waiting for health. The
    # restore helper handles each original container's exact healthcheck.
    while IFS= read -r file; do
        python3 "$state_helper" restore-start "$state_dir/pending/$file" || return 1
    done <"$state_dir/pending/order"
    while IFS= read -r file; do
        python3 "$state_helper" restore-wait "$state_dir/pending/$file" || return 1
    done <"$state_dir/pending/order"
    python3 "$state_helper" commit "$state_dir/pending"
}

rollback_on_exit() {
    local status=$?
    trap - EXIT
    if ((status != 0)) && [[ -d $state_dir/pending ]]; then
        echo "update-devshard: restoring the previous service specifications" >&2
        if ! restore_pending; then
            echo "update-devshard: recovery is incomplete; rerun the updater after fixing the failure ($state_dir/pending)" >&2
        fi
    fi
    exit "$status"
}

service_changed() {
    local service=$1 id=$2 current_hash desired_hash current_id desired_id
    [[ -n $id ]] || return 0
    current_id=$("$docker_bin" inspect --format '{{.Image}}' "$id") || fail "cannot inspect the image of $service"
    desired_id=$(desired_image_id "$service") || fail "cannot resolve the candidate image of $service"
    [[ $current_id == "$desired_id" ]] || return 0
    current_hash=$("$docker_bin" inspect --format '{{index .Config.Labels "com.docker.compose.config-hash"}}' "$id") || fail "cannot inspect the configuration of $service"
    desired_hash=$("${compose[@]}" config --hash "$service") || fail "cannot hash the candidate configuration of $service"
    desired_hash=${desired_hash##* }
    [[ -n $current_hash && $current_hash == "$desired_hash" ]] && return 1
    return 0
}

begin_replacement() {
    local service id name record health staging previous_tag
    [[ $dry_run == false ]] || return 0
    mkdir -p -- "$state_dir/previous"
    chmod 700 "$state_dir" "$state_dir/previous"
    staging=$(mktemp -d "$state_dir/pending.XXXXXX")
    for service in "$@"; do
        id=$("${compose[@]}" ps --all --quiet "$service") || fail "cannot list $service for rollback"
        [[ $id != *$'\n'* ]] || fail "$service has multiple containers; resolve the deployment before updating"
        name=$(jq -r --arg s "$service" --arg fallback "$project_name-$service-1" \
            '.services[$s].container_name // $fallback' <<<"$rendered")
        record=$staging/$service.json
        (umask 077; python3 "$state_helper" capture "$record" "$project_name" "$name" "$id") || fail "cannot save the rollback specification of $service"
        health=$(jq -r '.State.Health.Status // .State.Status // "absent"' "$record")
        if [[ $health == healthy || $health == running ]]; then
            if service_changed "$service" "$id"; then
                previous_tag=gonka-previous/${GONKA_DEPLOYMENT_KEY:0:16}/$service
                "$docker_bin" tag "$(jq -r .Image "$record")" "$previous_tag" || fail "cannot retain the previous image of $service"
                cp -- "$record" "$state_dir/previous/$service.json.tmp"
                mv -- "$state_dir/previous/$service.json.tmp" "$state_dir/previous/$service.json"
            fi
        elif [[ -f $state_dir/previous/$service.json ]]; then
            # Never make a failed candidate its own rollback destination.
            cp -- "$state_dir/previous/$service.json" "$record"
        fi
        printf '%s.json\n' "$service" >>"$staging/order"
    done
    python3 "$state_helper" publish "$staging" "$state_dir/pending" || fail "cannot publish the rollback record"
}

commit_replacement() {
    [[ $dry_run == false ]] || return 0
    python3 "$state_helper" commit "$state_dir/pending" || fail "cannot commit the replacement"
}
