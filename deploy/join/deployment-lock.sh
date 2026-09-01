#!/usr/bin/env bash

# Shared, re-entrant lock for every script that mutates one Gonka deployment.
# This file is sourced; do not change the caller's shell options.

gonka_acquire_deployment_lock() {
    local config_dir=$1

    GONKA_DEPLOYMENT_LOCK=${GONKA_DEPLOYMENT_LOCK:-$config_dir/.gonka-deployment.lock}
    export GONKA_DEPLOYMENT_LOCK

    # A parent updater keeps fd 9 open while invoking cutover/fleet helpers.
    # Verify the inherited descriptor instead of trusting the environment flag.
    if [[ ${GONKA_DEPLOYMENT_LOCK_HELD:-} == "$GONKA_DEPLOYMENT_LOCK" && \
        -e /proc/$$/fd/9 && /proc/$$/fd/9 -ef $GONKA_DEPLOYMENT_LOCK ]]; then
        return 0
    fi

    if [[ ! -e $GONKA_DEPLOYMENT_LOCK ]]; then
        (umask 077; : >"$GONKA_DEPLOYMENT_LOCK") || {
            echo "deployment-lock: cannot create $GONKA_DEPLOYMENT_LOCK" >&2
            return 1
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
