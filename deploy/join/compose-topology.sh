# shellcheck shell=bash
# Shared fail-closed Compose topology resolution for the v5 upgrade drivers.
# This file is sourced by upgrade-devshard-v5.sh and enable-router-ha.sh.

declare -ag GONKA_COMPOSE_FILES=()
declare -ag GONKA_COMPOSE_COMMAND=()
declare -ag GONKA_COMPOSE_FORWARD_ARGS=()
GONKA_COMPOSE_PROJECT=
GONKA_COMPOSE_PROJECT_DIRECTORY=
GONKA_COMPOSE_SOURCE=
GONKA_COMPOSE_POSTGRES_MODE=none
GONKA_COMPOSE_POSTGRES_HOST=
GONKA_COMPOSE_POSTGRES_DATA_DIR=
GONKA_COMPOSE_POLICY_NETWORK=
GONKA_COMPOSE_DEFAULT_NETWORK=
GONKA_COMPOSE_ROUTER_FRONT_NETWORK=
GONKA_COMPOSE_ROUTER_BACK_NETWORK=

gonka_compose_canonical_file() {
    local path=$1 base=$2 directory filename

    [[ $path != *$'\n'* && $path != *','* && $path != *$'\x1f'* ]] ||
        fail "Compose file names may not contain newlines, commas, or unit separators: $path"
    [[ $path == /* ]] || path=$base/$path
    [[ -f $path ]] || fail "Compose file does not exist: $path"
    directory=$(cd -- "$(dirname -- "$path")" && pwd -P)
    filename=$(basename -- "$path")
    printf '%s/%s\n' "$directory" "$filename"
}

gonka_compose_canonical_directory() {
    local path=$1 base=$2

    [[ $path == /* ]] || path=$base/$path
    [[ -d $path ]] || fail "Compose project directory does not exist: $path"
    (cd -- "$path" && pwd -P)
}

gonka_compose_append_unique() {
    local -n append_target=$1
    local value=$2 existing

    for existing in "${append_target[@]}"; do
        [[ $existing != "$value" ]] || return 0
    done
    append_target+=("$value")
}

gonka_compose_encode_files() {
    local -n files=$1
    local file encoded=

    for file in "${files[@]}"; do
        encoded+="${encoded:+$'\x1f'}$file"
    done
    printf '%s\n' "$encoded"
}

gonka_compose_decode_files() {
    local encoded=$1
    # shellcheck disable=SC2178 # The referenced variable is an array.
    local -n output_ref=$2

    output_ref=()
    [[ -n $encoded ]] || return 0
    IFS=$'\x1f' read -r -a output_ref <<<"$encoded"
}

gonka_compose_contains_sequence() {
    local candidate_encoded=$1 required_encoded=$2
    local -a candidate=() required=()
    local required_file
    local index=0 found

    gonka_compose_decode_files "$candidate_encoded" candidate
    gonka_compose_decode_files "$required_encoded" required
    for required_file in "${required[@]}"; do
        found=false
        while ((index < ${#candidate[@]})); do
            if [[ ${candidate[index]} == "$required_file" ]]; then
                found=true
                ((index += 1))
                break
            fi
            ((index += 1))
        done
        [[ $found == true ]] || return 1
    done
}

gonka_compose_parse_runtime_files() {
    local value=$1 working_dir=$2 script_dir=$3
    # shellcheck disable=SC2178 # The referenced variable is an array.
    local -n output_ref=$4
    local -a raw=()
    local file canonical
    local versiond_compat="$script_dir/docker-compose.versiond-v5-compat.yml"
    local edge_compat="$script_dir/docker-compose.edge-api-v5-compat.yml"
    local proxy_compat="$script_dir/docker-compose.proxy-v4-compat.yml"

    output_ref=()
    IFS=',' read -r -a raw <<<"$value"
    ((${#raw[@]} > 0)) || fail "running Compose project has an empty config_files label"
    for file in "${raw[@]}"; do
        [[ -n $file ]] || fail "running Compose project has an invalid config_files label: $value"
        canonical=$(gonka_compose_canonical_file "$file" "$working_dir")
        # These manifests are operation-local migration projections. Containers
        # recreated by an interrupted upgrade may carry them in Docker labels,
        # but they are not part of the operator's durable Compose topology.
        case $canonical in
            "$versiond_compat" | "$edge_compat" | "$proxy_compat") continue ;;
        esac
        gonka_compose_append_unique output_ref "$canonical"
    done
    ((${#output_ref[@]} > 0)) || fail \
        "running Compose project contains only transient compatibility files"
}

gonka_compose_runtime_metadata() {
    local docker_bin=$1 container=$2 script_dir=$3
    # shellcheck disable=SC2034 # Outputs are assigned through namerefs.
    local -n project_out=$4 working_dir_out=$5 files_out=$6
    local labels files_label
    local -a parsed_files=()

    labels=$("$docker_bin" inspect --format '{{json .Config.Labels}}' "$container") ||
        fail "cannot inspect Compose metadata for container $container"
    # shellcheck disable=SC2034 # Assign through the project_out nameref.
    project_out=$(jq -er \
        '.["com.docker.compose.project"] | strings | select(length > 0)' \
        <<<"$labels") || fail \
        "container $container has no com.docker.compose.project label; refusing to mutate a container not owned by a verifiable Compose project"
    working_dir_out=$(jq -er \
        '.["com.docker.compose.project.working_dir"] | strings | select(length > 0)' \
        <<<"$labels") || fail \
        "container $container has no Compose working-directory label"
    working_dir_out=$(gonka_compose_canonical_directory "$working_dir_out" /)
    files_label=$(jq -er \
        '.["com.docker.compose.project.config_files"] | strings | select(length > 0)' \
        <<<"$labels") || fail \
        "container $container has no Compose config-files label; the running topology cannot be verified"
    gonka_compose_parse_runtime_files \
        "$files_label" "$working_dir_out" "$script_dir" parsed_files
    # shellcheck disable=SC2034 # Assign through the files_out nameref.
    files_out=("${parsed_files[@]}")
}

# Explicit files are the recovery authority when a deployment checkout moved or
# an old override was deleted intentionally. Docker labels still prove project
# ownership, and every label path that remains readable is enforced as a
# required subsequence. Stale paths are reported but cannot veto the explicit
# model before Compose itself validates it.
gonka_compose_runtime_metadata_for_explicit() {
    local docker_bin=$1 container=$2 script_dir=$3
    # shellcheck disable=SC2178,SC2034 # Outputs are assigned through namerefs.
    local -n project_out=$4 working_dir_out=$5 files_out=$6
    local labels files_label raw_working file path directory filename
    local -a raw=()
    local versiond_compat="$script_dir/docker-compose.versiond-v5-compat.yml"
    local edge_compat="$script_dir/docker-compose.edge-api-v5-compat.yml"
    local proxy_compat="$script_dir/docker-compose.proxy-v4-compat.yml"

    labels=$("$docker_bin" inspect --format '{{json .Config.Labels}}' "$container") ||
        fail "cannot inspect Compose metadata for container $container"
    # shellcheck disable=SC2034 # Assign through the project_out nameref.
    project_out=$(jq -er \
        '.["com.docker.compose.project"] | strings | select(length > 0)' \
        <<<"$labels") || fail \
        "container $container has no com.docker.compose.project label; refusing to mutate a container not owned by a verifiable Compose project"
    raw_working=$(jq -er \
        '.["com.docker.compose.project.working_dir"] | strings | select(length > 0)' \
        <<<"$labels") || return 2
    [[ -d $raw_working ]] || return 2
    working_dir_out=$(cd -- "$raw_working" && pwd -P) || return 2
    files_label=$(jq -er \
        '.["com.docker.compose.project.config_files"] | strings | select(length > 0)' \
        <<<"$labels") || return 2
    IFS=',' read -r -a raw <<<"$files_label"
    ((${#raw[@]} > 0)) || return 2
    files_out=()
    for file in "${raw[@]}"; do
        [[ -n $file && $file != *$'\n'* && $file != *$'\x1f'* ]] || return 2
        path=$file
        [[ $path == /* ]] || path=$working_dir_out/$path
        [[ -f $path ]] || return 2
        directory=$(cd -- "$(dirname -- "$path")" && pwd -P) || return 2
        filename=$(basename -- "$path")
        path=$directory/$filename
        case $path in
            "$versiond_compat" | "$edge_compat" | "$proxy_compat") continue ;;
        esac
        gonka_compose_append_unique files_out "$path"
    done
    ((${#files_out[@]} > 0)) || return 2
}

gonka_compose_container_env() {
    local docker_bin=$1 container=$2 name=$3 line

    while IFS= read -r line; do
        case $line in
            "$name="*)
                printf '%s\n' "${line#*=}"
                return 0
                ;;
        esac
    done < <("$docker_bin" inspect --format \
        '{{range .Config.Env}}{{println .}}{{end}}' "$container")
    return 1
}

gonka_compose_require_service() {
    local config=$1 service=$2 expected_container=${3-} actual

    jq -e --arg service "$service" '.services | has($service)' \
        >/dev/null <<<"$config" || fail \
        "resolved Compose topology does not define required service $service"
    [[ -n $expected_container ]] || return 0
    actual=$(jq -r --arg service "$service" \
        '.services[$service].container_name // ""' <<<"$config")
    [[ $actual == "$expected_container" ]] || fail \
        "service $service must keep container_name=$expected_container for the upgrade driver; resolved value is '${actual:-unset}'"
}

gonka_compose_validate_postgres_identity() {
    local docker_bin=$1 config=$2
    shift 2
    local -a runtime_containers=("$@")
    local versiond_host versiond2_host storage_mode container key expected actual

    versiond_host=$(jq -r '.services.versiond.environment.PGHOST // ""' <<<"$config")
    versiond2_host=$(jq -r '.services.versiond2.environment.PGHOST // ""' <<<"$config")
    [[ -n $versiond_host && $versiond_host == "$versiond2_host" ]] || fail \
        "HA versiond services must resolve the same non-empty PGHOST; got versiond='${versiond_host:-unset}', versiond2='${versiond2_host:-unset}'"
    for container in versiond versiond2; do
        [[ " ${runtime_containers[*]} " == *" $container "* ]] || continue
        "$docker_bin" inspect "$container" >/dev/null 2>&1 || continue
        for key in PGHOST PGDATABASE PGUSER; do
            expected=$(jq -r --arg service "$container" --arg key "$key" \
                '.services[$service].environment[$key] // ""' <<<"$config")
            actual=$(gonka_compose_container_env \
                "$docker_bin" "$container" "$key") || actual=
            [[ -n $expected && -n $actual && $actual == "$expected" ]] || fail \
                "resolved Compose topology changes $container $key from '${actual:-unset}' to '${expected:-unset}'; refuse an implicit PostgreSQL identity change"
        done
        expected=$(jq -r --arg service "$container" \
            '.services[$service].environment.PGPORT // "5432"' <<<"$config")
        actual=$(gonka_compose_container_env \
            "$docker_bin" "$container" PGPORT) || actual=5432
        [[ -n $actual ]] || actual=5432
        [[ $actual == "$expected" ]] || fail \
            "resolved Compose topology changes $container PGPORT from '$actual' to '$expected'; refuse an implicit PostgreSQL identity change"
    done

    storage_mode=$(jq -r '.services.versiond.environment.DEVSHARD_STORAGE_MODE // ""' \
        <<<"$config")
    [[ $storage_mode == postgres && \
        $(jq -r '.services.versiond2.environment.DEVSHARD_STORAGE_MODE // ""' \
            <<<"$config") == postgres ]] || fail \
        "HA versiond services must use DEVSHARD_STORAGE_MODE=postgres"

    GONKA_COMPOSE_POSTGRES_HOST=$versiond_host
    if [[ $versiond_host == devshard-postgres ]]; then
        gonka_compose_require_service "$config" devshard-postgres devshard-postgres
        GONKA_COMPOSE_POSTGRES_DATA_DIR=$(jq -er '
            [.services["devshard-postgres"].volumes[]?
             | select(.target == "/var/lib/postgresql/gonka")]
            | select(length == 1 and .[0].type == "bind")
            | .[0].source
        ' <<<"$config") || fail \
            "local devshard-postgres must have exactly one bind mount at /var/lib/postgresql/gonka"
        GONKA_COMPOSE_POSTGRES_MODE=local
    else
        GONKA_COMPOSE_POSTGRES_DATA_DIR=
        GONKA_COMPOSE_POSTGRES_MODE=external
    fi
}

gonka_compose_resolve() {
    local docker_bin=$1 script_dir=$2 versiond_mode=$3 edge_mode=$4
    local project_override=$5 directory_override=$6
    local -n explicit_file_args=$7 runtime_container_args=$8
    local invocation_dir=$PWD
    local container project working_dir encoded candidate required metadata_status
    local runtime_project='' runtime_working_dir='' selected_encoded='' source=''
    local best_count=-1 all_match count
    local -a runtime_files=() observed=() requested=() selected=()
    local -a raw_requested=()
    local file separator config config_project

    if ((${#explicit_file_args[@]} > 0)); then
        raw_requested=("${explicit_file_args[@]}")
        source='command line'
    elif [[ -n ${COMPOSE_FILE-} ]]; then
        separator=${COMPOSE_PATH_SEPARATOR:-:}
        [[ ${#separator} -eq 1 ]] || fail \
            "COMPOSE_PATH_SEPARATOR must contain exactly one character"
        IFS=$separator read -r -a raw_requested <<<"$COMPOSE_FILE"
        source='COMPOSE_FILE'
    fi
    if ((${#raw_requested[@]} > 0)); then
        for file in "${raw_requested[@]}"; do
            [[ -n $file ]] || fail "explicit Compose file list contains an empty entry"
            gonka_compose_append_unique requested \
                "$(gonka_compose_canonical_file "$file" "$invocation_dir")"
        done
        selected=("${requested[@]}")
    fi

    for container in "${runtime_container_args[@]}"; do
        "$docker_bin" inspect "$container" >/dev/null 2>&1 || continue
        metadata_status=0
        if ((${#selected[@]} > 0)); then
            gonka_compose_runtime_metadata_for_explicit \
                "$docker_bin" "$container" "$script_dir" \
                project working_dir runtime_files || metadata_status=$?
        else
            gonka_compose_runtime_metadata \
                "$docker_bin" "$container" "$script_dir" \
                project working_dir runtime_files
        fi
        if [[ -z $runtime_project ]]; then
            runtime_project=$project
        else
            [[ $project == "$runtime_project" ]] || fail \
                "containers in the upgrade scope belong to different Compose projects: $runtime_project and $project"
        fi
        if ((metadata_status != 0)); then
            warn "container $container records stale Compose file metadata; validating the explicit topology instead"
            continue
        fi
        if [[ -z $runtime_working_dir ]]; then
            runtime_working_dir=$working_dir
        elif [[ $working_dir != "$runtime_working_dir" && ${#selected[@]} -eq 0 ]]; then
            fail "containers in Compose project $project record different working directories; pass a verified complete topology explicitly"
        fi
        encoded=$(gonka_compose_encode_files runtime_files)
        observed+=("$encoded")
    done

    if ((${#selected[@]} > 0)); then
        selected_encoded=$(gonka_compose_encode_files selected)
        for required in "${observed[@]}"; do
            gonka_compose_contains_sequence "$selected_encoded" "$required" || fail \
                "explicit Compose topology omits or reorders a file recorded by running containers; include the complete existing model before adding overrides"
        done
    else
        ((${#observed[@]} > 0)) || fail \
            "cannot recover Compose topology from running containers; pass --compose-file for every file in the deployment model"
        for candidate in "${observed[@]}"; do
            all_match=true
            for required in "${observed[@]}"; do
                if ! gonka_compose_contains_sequence "$candidate" "$required"; then
                    all_match=false
                    break
                fi
            done
            [[ $all_match == true ]] || continue
            gonka_compose_decode_files "$candidate" runtime_files
            count=${#runtime_files[@]}
            if ((count > best_count)); then
                selected_encoded=$candidate
                best_count=$count
            fi
        done
        [[ -n $selected_encoded ]] || fail \
            "running containers record incompatible Compose file sets; rerun with the complete ordered list via --compose-file or COMPOSE_FILE"
        gonka_compose_decode_files "$selected_encoded" selected
        source='Docker Compose labels'
    fi

    if [[ -z $project_override ]]; then
        project_override=${COMPOSE_PROJECT_NAME-}
    fi
    if [[ -n $project_override && $project_override != "$runtime_project" ]]; then
        fail "Compose project override '$project_override' does not match running project '$runtime_project'"
    fi
    GONKA_COMPOSE_PROJECT=$runtime_project

    if [[ -n $directory_override ]]; then
        GONKA_COMPOSE_PROJECT_DIRECTORY=$(gonka_compose_canonical_directory \
            "$directory_override" "$invocation_dir")
        if ((${#selected[@]} == 0)); then
            [[ $GONKA_COMPOSE_PROJECT_DIRECTORY == "$runtime_working_dir" ]] || fail \
                "Compose project-directory override '$GONKA_COMPOSE_PROJECT_DIRECTORY' does not match running project directory '$runtime_working_dir'"
        fi
    elif [[ -n $runtime_working_dir ]]; then
        GONKA_COMPOSE_PROJECT_DIRECTORY=$runtime_working_dir
    else
        GONKA_COMPOSE_PROJECT_DIRECTORY=$(dirname -- "${selected[0]}")
    fi
    GONKA_COMPOSE_FILES=("${selected[@]}")
    GONKA_COMPOSE_SOURCE=$source
    GONKA_COMPOSE_COMMAND=(
        "$docker_bin" compose
        --project-directory "$GONKA_COMPOSE_PROJECT_DIRECTORY"
        --project-name "$GONKA_COMPOSE_PROJECT"
    )
    GONKA_COMPOSE_FORWARD_ARGS=(
        --compose-project-directory "$GONKA_COMPOSE_PROJECT_DIRECTORY"
        --compose-project-name "$GONKA_COMPOSE_PROJECT"
    )
    for file in "${GONKA_COMPOSE_FILES[@]}"; do
        GONKA_COMPOSE_COMMAND+=(-f "$file")
        GONKA_COMPOSE_FORWARD_ARGS+=(--compose-file "$file")
    done

    config=$("${GONKA_COMPOSE_COMMAND[@]}" config --format json) || fail \
        "resolved Compose topology does not render successfully"
    config_project=$(jq -er '.name | strings | select(length > 0)' <<<"$config") ||
        fail "resolved Compose model has no project name"
    [[ $config_project == "$GONKA_COMPOSE_PROJECT" ]] || fail \
        "resolved Compose model renders project '$config_project', expected '$GONKA_COMPOSE_PROJECT'"

    gonka_compose_require_service "$config" proxy proxy
    gonka_compose_require_service "$config" proxy-policy
    gonka_compose_require_service "$config" versiond versiond
    gonka_compose_require_service "$config" edge-api edge-api
    # shellcheck disable=SC2034 # Exported to the sourcing upgrade driver.
    GONKA_COMPOSE_POLICY_NETWORK=$(jq -er \
        '.networks["proxy-policy-front"].name | strings | select(length > 0)' \
        <<<"$config") || fail \
        "resolved Compose topology has no proxy-policy-front network"
    # shellcheck disable=SC2034 # Exported to the sourcing upgrade drivers.
    GONKA_COMPOSE_DEFAULT_NETWORK=$(jq -er \
        '.networks.default.name | strings | select(length > 0)' \
        <<<"$config") || fail \
        "resolved Compose topology has no default network for internal router metrics"
    if [[ $versiond_mode == ha ]]; then
        gonka_compose_require_service "$config" versiond2 versiond2
        GONKA_COMPOSE_ROUTER_FRONT_NETWORK=$(jq -er \
            '.networks["versiond-router-front"].name | strings | select(length > 0)' \
            <<<"$config") || fail \
            "resolved Compose topology has no versiond-router-front network"
        GONKA_COMPOSE_ROUTER_BACK_NETWORK=$(jq -er \
            '.networks["versiond-router-back"].name | strings | select(length > 0)' \
            <<<"$config") || fail \
            "resolved Compose topology has no versiond-router-back network"
        gonka_compose_validate_postgres_identity \
            "$docker_bin" "$config" "${runtime_container_args[@]}"
    else
        GONKA_COMPOSE_POSTGRES_MODE=none
        GONKA_COMPOSE_POSTGRES_HOST=
        # shellcheck disable=SC2034 # Exported to the sourcing upgrade driver.
        GONKA_COMPOSE_POSTGRES_DATA_DIR=
        # shellcheck disable=SC2034 # Exported to the sourcing upgrade driver.
        GONKA_COMPOSE_ROUTER_FRONT_NETWORK=
        # shellcheck disable=SC2034 # Exported to the sourcing upgrade driver.
        GONKA_COMPOSE_ROUTER_BACK_NETWORK=
    fi
    if [[ $edge_mode == multi ]]; then
        gonka_compose_require_service "$config" edge-api2 edge-api2
        gonka_compose_require_service "$config" edge-api3 edge-api3
    fi

    printf 'Compose topology: project=%s source=%s files=%s\n' \
        "$GONKA_COMPOSE_PROJECT" "$GONKA_COMPOSE_SOURCE" \
        "${GONKA_COMPOSE_FILES[*]}"
    if [[ $GONKA_COMPOSE_POSTGRES_MODE == external ]]; then
        printf 'Compose topology: preserving external devshard PostgreSQL host %s\n' \
            "$GONKA_COMPOSE_POSTGRES_HOST"
    fi
}
