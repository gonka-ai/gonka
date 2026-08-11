#!/usr/bin/env bash

# Shared release/source and host-capacity checks for upgrade-devshard-v5.sh.
# This file is sourced; do not change the caller's shell options.

devshard_v5_contract_error() {
    echo "devshard-v5-release-contract: $*" >&2
    return 1
}

devshard_v5_contract_warn() {
    echo "devshard-v5-release-contract: warning: $*" >&2
}

devshard_v5_load_release_contract() {
    local contract_file=$1
    local name

    [[ -f $contract_file ]] || devshard_v5_contract_error \
        "release contract not found: $contract_file" || return
    # shellcheck disable=SC1090
    source "$contract_file"
    for name in \
        DEVSHARD_V5_RELEASE_ID \
        DEVSHARD_V5_RELEASE_GIT_TAG \
        DEVSHARD_V5_RELEASE_IMAGE_TAG \
        DEVSHARD_V5_EDGE_API_IMAGE \
        DEVSHARD_V5_VERSIOND_IMAGE \
        DEVSHARD_V5_EDGE_API_ROUTER_IMAGE \
        DEVSHARD_V5_VERSIOND_ROUTER_IMAGE \
        DEVSHARD_V5_PROXY_POLICY_IMAGE \
        DEVSHARD_V5_PROXY_ROUTER_IMAGE \
        DEVSHARD_V5_POSTGRES_IMAGE \
        DEVSHARD_V5_MIN_COMPOSE_VERSION \
        DEVSHARD_V5_RECOMMENDED_CPUS \
        DEVSHARD_V5_RECOMMENDED_MEMORY_MIB; do
        [[ -n ${!name:-} ]] || devshard_v5_contract_error \
            "release contract has no $name" || return
    done
    [[ $DEVSHARD_V5_POSTGRES_IMAGE =~ ^[^[:space:]@]+@sha256:[0-9a-f]{64}$ ]] || \
        devshard_v5_contract_error \
            "DEVSHARD_V5_POSTGRES_IMAGE must use an immutable sha256 digest" || \
        return
    [[ $DEVSHARD_V5_RELEASE_ID =~ ^[A-Za-z0-9._-]+$ ]] || \
        devshard_v5_contract_error "invalid release ID" || return
    [[ $DEVSHARD_V5_RELEASE_GIT_TAG == release/* ]] || \
        devshard_v5_contract_error "release Git tag must start with release/" || \
        return
    [[ $DEVSHARD_V5_RELEASE_IMAGE_TAG =~ ^[A-Za-z0-9._-]+$ ]] || \
        devshard_v5_contract_error "invalid release image tag" || return
    for name in \
        DEVSHARD_V5_EDGE_API_IMAGE \
        DEVSHARD_V5_VERSIOND_IMAGE \
        DEVSHARD_V5_EDGE_API_ROUTER_IMAGE \
        DEVSHARD_V5_VERSIOND_ROUTER_IMAGE \
        DEVSHARD_V5_PROXY_POLICY_IMAGE \
        DEVSHARD_V5_PROXY_ROUTER_IMAGE \
        DEVSHARD_V5_POSTGRES_IMAGE; do
        [[ ${!name} == *":$DEVSHARD_V5_RELEASE_IMAGE_TAG" || \
            ${!name} =~ ^[^[:space:]@]+@sha256:[0-9a-f]{64}$ ]] || \
            devshard_v5_contract_error \
                "$name must use release tag $DEVSHARD_V5_RELEASE_IMAGE_TAG during staging or an immutable sha256 digest" || \
            return
    done
}

devshard_v5_verify_release_image_digests() {
    local name

    for name in \
        DEVSHARD_V5_EDGE_API_IMAGE \
        DEVSHARD_V5_VERSIOND_IMAGE \
        DEVSHARD_V5_EDGE_API_ROUTER_IMAGE \
        DEVSHARD_V5_VERSIOND_ROUTER_IMAGE \
        DEVSHARD_V5_PROXY_POLICY_IMAGE \
        DEVSHARD_V5_PROXY_ROUTER_IMAGE \
        DEVSHARD_V5_POSTGRES_IMAGE; do
        [[ ${!name} =~ ^[^[:space:]@]+@sha256:[0-9a-f]{64}$ ]] || \
            devshard_v5_contract_error \
                "$name must be pinned as repo@sha256:digest before publication" || \
            return
    done
}

devshard_v5_version_at_least() {
    local actual=${1#v} required=${2#v}
    local actual_core required_core

    actual_core=${actual%%[-+]*}
    required_core=${required%%[-+]*}
    [[ $actual_core =~ ^[0-9]+\.[0-9]+\.[0-9]+$ && \
        $required_core =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || return 1
    [[ $(printf '%s\n%s\n' "$required_core" "$actual_core" | \
        sort -V | head -n1) == "$required_core" ]]
}

devshard_v5_verify_dependencies() {
    local docker_bin=$1 command compose_version

    for command in jq curl flock sha256sum git getconf awk df timeout python3 sync; do
        command -v "$command" >/dev/null 2>&1 || \
            devshard_v5_contract_error "$command is required" || return
    done
    command -v "$docker_bin" >/dev/null 2>&1 || \
        devshard_v5_contract_error "$docker_bin is required" || return
    timeout 15 "$docker_bin" info >/dev/null 2>&1 || devshard_v5_contract_error \
        "cannot reach the Docker daemon with $docker_bin" || return
    compose_version=$(
        timeout 15 "$docker_bin" compose version --short 2>/dev/null
    ) || devshard_v5_contract_error \
        "Docker Compose v2 is required" || return
    devshard_v5_version_at_least \
        "$compose_version" "$DEVSHARD_V5_MIN_COMPOSE_VERSION" || \
        devshard_v5_contract_error \
            "Docker Compose $DEVSHARD_V5_MIN_COMPOSE_VERSION or newer is required; found ${compose_version:-unknown}" || \
        return
    printf 'Release preflight: Docker Compose %s\n' "$compose_version"
}

devshard_v5_release_critical_paths() {
    cat <<'EOF'
deploy/join/devshard-v5-release.env
deploy/join/devshard-v5-release-contract.sh
deploy/join/devshard-v5-release-gate.sh
deploy/join/upgrade-devshard-v5.sh
deploy/join/deployment-lock.sh
deploy/join/compose-topology.sh
deploy/join/devshard-postgres-entrypoint.sh
deploy/join/devshard-postgres-migration-preflight.sh
deploy/join/legacy-router-upgrade-barrier.sh
deploy/join/enable-router-ha.sh
deploy/join/versiond-router-fleet.sh
deploy/join/docker-compose.versiond-v5-compat.yml
deploy/join/docker-compose.edge-api-v5-compat.yml
deploy/join/docker-compose.proxy-v4-compat.yml
deploy/join/versiond-router-slot/docker-compose.yml
router-runtime/catalog-reconciler
router-runtime/catalog-status
router-runtime/catalog-cache
EOF
}

devshard_v5_verify_release_source() {
    local script_dir=$1 allow_unreleased=${DEVSHARD_V5_ALLOW_UNRELEASED_SOURCE:-false}
    local repo_root tag_ref release_commit path expected_blob actual_blob

    case $allow_unreleased in
        true | 1 | yes)
            devshard_v5_contract_warn \
                "unreleased source verification is disabled explicitly"
            DEVSHARD_V5_RELEASE_COMMIT=unreleased
            return 0
            ;;
        false | 0 | no) ;;
        *) devshard_v5_contract_error \
            "DEVSHARD_V5_ALLOW_UNRELEASED_SOURCE must be true or false" || return ;;
    esac

    devshard_v5_verify_release_image_digests || return

    repo_root=$(git -C "$script_dir" rev-parse --show-toplevel 2>/dev/null) || \
        devshard_v5_contract_error \
            "the updater must run from a Git checkout containing $DEVSHARD_V5_RELEASE_GIT_TAG" || \
        return
    tag_ref=refs/tags/$DEVSHARD_V5_RELEASE_GIT_TAG
    release_commit=$(git -C "$repo_root" rev-parse --verify \
        "$tag_ref^{commit}" 2>/dev/null) || devshard_v5_contract_error \
        "release tag $DEVSHARD_V5_RELEASE_GIT_TAG is missing; fetch the immutable tag before upgrading" || \
        return

    while IFS= read -r path; do
        [[ -n $path ]] || continue
        expected_blob=$(git -C "$repo_root" rev-parse \
            "$tag_ref:$path" 2>/dev/null) || devshard_v5_contract_error \
            "release tag does not contain required file $path" || return
        [[ -f $repo_root/$path ]] || devshard_v5_contract_error \
            "required release file is missing from the working tree: $path" || \
            return
        actual_blob=$(git -C "$repo_root" hash-object -- "$repo_root/$path") || \
            return
        [[ $actual_blob == "$expected_blob" ]] || \
            devshard_v5_contract_error \
                "$path differs from immutable release $DEVSHARD_V5_RELEASE_GIT_TAG; restore the tagged file before upgrading" || \
            return
    done < <(devshard_v5_release_critical_paths)

    DEVSHARD_V5_RELEASE_COMMIT=$release_commit
    printf 'Release source: %s (%s)\n' \
        "$DEVSHARD_V5_RELEASE_GIT_TAG" "$DEVSHARD_V5_RELEASE_COMMIT"
}

devshard_v5_report_compose_files() {
    local script_dir=$1
    shift
    local repo_root='' tag_ref='' file relative expected_blob actual_blob digest kind

    repo_root=$(git -C "$script_dir" rev-parse --show-toplevel 2>/dev/null || true)
    if [[ -n $repo_root && $DEVSHARD_V5_RELEASE_COMMIT != unreleased ]]; then
        tag_ref=refs/tags/$DEVSHARD_V5_RELEASE_GIT_TAG
    fi

    echo "Release preflight: effective Compose files"
    for file in "$@"; do
        digest=$(sha256sum -- "$file" | awk '{print $1}') || return
        kind=custom
        if [[ -n $repo_root && $file == "$repo_root/"* ]]; then
            relative=${file#"$repo_root/"}
            if [[ -n $tag_ref ]] && expected_blob=$(git -C "$repo_root" \
                rev-parse "$tag_ref:$relative" 2>/dev/null); then
                actual_blob=$(git -C "$repo_root" hash-object -- "$file") || return
                if [[ $actual_blob == "$expected_blob" ]]; then
                    kind=release
                else
                    kind=local-override
                fi
            else
                kind=working-tree
            fi
        fi
        printf '  %s  sha256=%s  source=%s\n' "$file" "$digest" "$kind"
    done
}

devshard_v5_capacity_value() {
    local override=$1
    shift
    if [[ -n $override ]]; then
        printf '%s\n' "$override"
    else
        "$@"
    fi
}

devshard_v5_memory_mib() {
    awk '/^MemTotal:/ {print int($2 / 1024)}' /proc/meminfo
}

devshard_v5_verify_capacity() {
    local config_dir=$1 strict=$2
    local cpus memory_mib failures=0

    # Disk migration safety is checked against the actual PostgreSQL target
    # later, using source bytes plus reserve. The checkout filesystem is not a
    # valid proxy for that requirement.
    : "$config_dir"

    cpus=$(devshard_v5_capacity_value \
        "${DEVSHARD_V5_PREFLIGHT_CPUS:-}" getconf _NPROCESSORS_ONLN)
    memory_mib=$(devshard_v5_capacity_value \
        "${DEVSHARD_V5_PREFLIGHT_MEMORY_MIB:-}" \
        devshard_v5_memory_mib)
    [[ $cpus =~ ^[1-9][0-9]*$ ]] || devshard_v5_contract_error \
        "cannot determine online CPU count" || return
    [[ $memory_mib =~ ^[1-9][0-9]*$ ]] || devshard_v5_contract_error \
        "cannot determine host memory" || return
    printf 'Release preflight: host compute capacity cpu=%s memory=%sMiB\n' \
        "$cpus" "$memory_mib"
    if ((cpus < DEVSHARD_V5_RECOMMENDED_CPUS)); then
        devshard_v5_contract_warn \
            "host has $cpus CPUs; Quickstart recommends $DEVSHARD_V5_RECOMMENDED_CPUS"
        failures=$((failures + 1))
    fi
    if ((memory_mib < DEVSHARD_V5_RECOMMENDED_MEMORY_MIB)); then
        devshard_v5_contract_warn \
            "host has ${memory_mib}MiB RAM; Quickstart recommends ${DEVSHARD_V5_RECOMMENDED_MEMORY_MIB}MiB"
        failures=$((failures + 1))
    fi
    if [[ $strict == true && $failures -gt 0 ]]; then
        devshard_v5_contract_error \
            "host capacity is below the release recommendation; fix capacity or rerun without --strict-capacity" || \
            return
    fi
}

devshard_v5_validate_port() {
    local name=$1 value=$2
    [[ $value =~ ^[0-9]+$ && $value -ge 1 && $value -le 65535 ]] || \
        devshard_v5_contract_error "$name must be an integer from 1 to 65535"
}

devshard_v5_verify_public_ports() {
    local docker_bin=$1 proxy_container=$2 http_port=$3 https_port=$4
    local bindings container_port expected

    devshard_v5_validate_port API_PORT "$http_port" || return
    if [[ -n $https_port ]]; then
        devshard_v5_validate_port API_SSL_PORT "$https_port" || return
        [[ $http_port != "$https_port" ]] || devshard_v5_contract_error \
            "API_PORT and API_SSL_PORT must be different" || return
    fi
    bindings=$(timeout 15 "$docker_bin" inspect --format \
        '{{json .HostConfig.PortBindings}}' "$proxy_container") || \
        devshard_v5_contract_error \
            "cannot inspect public port ownership on $proxy_container" || return
    for container_port in 80/tcp 443/tcp; do
        if [[ $container_port == 80/tcp ]]; then
            expected=$http_port
        else
            expected=$https_port
            [[ -n $expected ]] || continue
        fi
        jq -e --arg container_port "$container_port" --arg expected "$expected" '
            (.[$container_port] // [])
            | any(.HostPort == $expected)
        ' <<<"$bindings" >/dev/null || devshard_v5_contract_error \
            "$proxy_container does not own expected host port $expected for $container_port" || \
            return
    done
    if [[ -n $https_port ]]; then
        printf 'Release preflight: public ports %s/http and %s/https are owned by %s\n' \
            "$http_port" "$https_port" "$proxy_container"
    else
        printf 'Release preflight: public port %s/http is owned by %s; HTTPS is disabled\n' \
            "$http_port" "$proxy_container"
    fi
}
