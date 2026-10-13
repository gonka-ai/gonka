#!/usr/bin/env bash

# Shared, re-entrant lock for every script that mutates one Gonka deployment.
# This file is sourced; do not change the caller's shell options.

gonka_acquire_deployment_lock() {
    local config_dir=$1 project=${2:-${GONKA_DEPLOYMENT_PROJECT:-${COMPOSE_PROJECT_NAME:-}}}
    local daemon owner output key
    if [[ -z ${GONKA_DEPLOYMENT_LOCK:-} ]]; then
        daemon=$("${DOCKER_BIN:-docker}" info --format '{{.ID}}') || {
            echo "deployment-lock: cannot identify the Docker daemon" >&2
            return 1
        }
        [[ -n $daemon ]] || { echo "deployment-lock: empty Docker daemon identity" >&2; return 1; }
        if [[ -z $project ]]; then
            for owner in "${PROXY_ROUTER_CONTAINER:-proxy}" versiond; do
                if output=$("${DOCKER_BIN:-docker}" inspect --format \
                    '{{index .Config.Labels "com.docker.compose.project"}}' "$owner" 2>&1); then
                    [[ -n $output && $output != '<no value>' ]] && { project=$output; break; }
                else
                    case ${output,,} in
                        *'no such object'*|*'no such container'*) ;;
                        *) echo "deployment-lock: cannot inspect $owner: $output" >&2; return 1 ;;
                    esac
                fi
            done
        fi
        project=${project:-$(basename -- "${script_dir:-$config_dir}")}
        key=$(printf '%s\n%s\n' "$daemon" "$project" | sha256sum) || return 1
        GONKA_DEPLOYMENT_KEY=${key%% *}
        GONKA_DEPLOYMENT_PROJECT=$project
        GONKA_DEPLOYMENT_LOCK=/tmp/gonka-deployment-$GONKA_DEPLOYMENT_KEY.lock
        export GONKA_DEPLOYMENT_KEY GONKA_DEPLOYMENT_PROJECT
    elif [[ -z ${GONKA_DEPLOYMENT_KEY:-} ]]; then
        key=$(printf '%s\n' "$GONKA_DEPLOYMENT_LOCK" | sha256sum) || return 1
        GONKA_DEPLOYMENT_KEY=${key%% *}
        export GONKA_DEPLOYMENT_KEY
    fi
    export GONKA_DEPLOYMENT_LOCK

    # A parent updater keeps fd 9 open while invoking cutover/fleet helpers.
    # Verify the inherited descriptor instead of trusting the environment flag.
    if [[ ${GONKA_DEPLOYMENT_LOCK_HELD:-} == "$GONKA_DEPLOYMENT_LOCK" && \
        -e /proc/$$/fd/9 && /proc/$$/fd/9 -ef $GONKA_DEPLOYMENT_LOCK ]]; then
        return 0
    fi

    if [[ ! -e $GONKA_DEPLOYMENT_LOCK ]]; then
        (umask 022; set -o noclobber; : >"$GONKA_DEPLOYMENT_LOCK") || {
            [[ -e $GONKA_DEPLOYMENT_LOCK ]] || {
                echo "deployment-lock: cannot create $GONKA_DEPLOYMENT_LOCK" >&2
                return 1
            }
        }
    fi
    exec 9<"$GONKA_DEPLOYMENT_LOCK"
    flock -n 9 || {
        echo "deployment-lock: another deployment operation holds $GONKA_DEPLOYMENT_LOCK" >&2
        return 1
    }
    GONKA_DEPLOYMENT_LOCK_HELD=$GONKA_DEPLOYMENT_LOCK
    export GONKA_DEPLOYMENT_LOCK_HELD
}
