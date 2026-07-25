#!/usr/bin/env bash
# Shared helpers: source join env files and discover docker compose file stack.
# Intended to be sourced from prepare-compose-restart.sh or other operator scripts.
#
# After sourcing gonka_compose_prepare:
#   - JOIN_DIR, COMPOSE_FILES, COMPOSE_FILE_ARRAY
#   - dc() — docker compose with the full override stack
#   - devshard_dc() — devshardctl compose (when config.devshard.env exists)
#
# Optional env overrides:
#   GONKA_DEPLOY_DIR / JOIN_DIR — deploy directory (default: parent of scripts/)
#   GONKA_SKIP_DEVSHARD_ENV=1   — do not source config.devshard.env
#   GONKA_INCLUDE_DEVSHARD_PROXY=auto|yes|no — devshard-proxy override (default: auto)

gonka_compose__die() {
  echo "[compose-env] ERROR: $*" >&2
  return 1
}

gonka_compose_resolve_join_dir() {
  if [[ -n "${JOIN_DIR:-}" ]]; then
    :
  elif [[ -n "${GONKA_DEPLOY_DIR:-}" ]]; then
    JOIN_DIR="$GONKA_DEPLOY_DIR"
  else
    local lib_dir
    lib_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    JOIN_DIR="$(cd "${lib_dir}/../.." && pwd)"
  fi

  if [[ ! -f "${JOIN_DIR}/docker-compose.yml" ]]; then
    for candidate in \
      "/srv/dai/gonka/deploy/join" \
      "/srv/gonka/deploy/join" \
      "/home/ubuntu/gonka/deploy/join"; do
      if [[ -f "${candidate}/docker-compose.yml" ]]; then
        JOIN_DIR="$candidate"
        break
      fi
    done
  fi

  [[ -n "${JOIN_DIR:-}" ]] || {
    gonka_compose__die "could not resolve JOIN_DIR — cd to deploy/join or set GONKA_DEPLOY_DIR"
    return 1
  }
  [[ -f "${JOIN_DIR}/docker-compose.yml" ]] || {
    gonka_compose__die "missing ${JOIN_DIR}/docker-compose.yml"
    return 1
  }
}

gonka_compose_source_env_files() {
  local skip_devshard="${GONKA_SKIP_DEVSHARD_ENV:-0}"

  cd "$JOIN_DIR" || return 1

  [[ -f "${JOIN_DIR}/config.env" ]] || {
    gonka_compose__die "missing ${JOIN_DIR}/config.env"
    return 1
  }

  # Export all config.env variables for docker compose substitution.
  set -a
  # shellcheck disable=SC1091
  source "${JOIN_DIR}/config.env"
  set +a

  if [[ "$skip_devshard" != "1" && -f "${JOIN_DIR}/config.devshard.env" ]]; then
    set -a
    # shellcheck disable=SC1091
    source "${JOIN_DIR}/config.devshard.env"
    set +a
    GONKA_DEVSHARD_ENV_SOURCED=1
  else
    GONKA_DEVSHARD_ENV_SOURCED=0
  fi

  if [[ -f "${JOIN_DIR}/.env" ]]; then
    set -a
    # shellcheck disable=SC1091
    source "${JOIN_DIR}/.env"
    set +a
    GONKA_DOTENV_SOURCED=1
  else
    GONKA_DOTENV_SOURCED=0
  fi

  # Common devshard multi-gateway default when .env is absent.
  if [[ -z "${DEVSHARD_INSTANCE_NAME:-}" && "$GONKA_DEVSHARD_ENV_SOURCED" == "1" ]]; then
    export DEVSHARD_INSTANCE_NAME="${DEVSHARD_INSTANCE_NAME:-devshardctl-multi}"
  fi

  export JOIN_DIR
  export GONKA_CHAIN_ID="${CHAIN_ID:-unknown}"
  export GONKA_KEY_NAME="${KEY_NAME:-}"
  export GONKA_API_PORT="${API_PORT:-8000}"
}

gonka_compose_discover_files() {
  local include_proxy="${GONKA_INCLUDE_DEVSHARD_PROXY:-auto}"
  local -a files=()

  files+=(-f docker-compose.yml)

  [[ -f docker-compose.mlnode.yml ]] && files+=(-f docker-compose.mlnode.yml)
  [[ -f docker-compose.env-override.yml ]] && files+=(-f docker-compose.env-override.yml)
  [[ -f docker-compose.genesis-override.yml ]] && files+=(-f docker-compose.genesis-override.yml)
  [[ -f docker-compose.runtime-override.yml ]] && files+=(-f docker-compose.runtime-override.yml)

  case "$include_proxy" in
    yes|1|true)
      [[ -f docker-compose.devshard-proxy-override.yml ]] \
        && files+=(-f docker-compose.devshard-proxy-override.yml)
      ;;
    auto|"")
      [[ -f docker-compose.devshard-proxy-override.yml ]] \
        && files+=(-f docker-compose.devshard-proxy-override.yml)
      ;;
    no|0|false)
      ;;
    *)
      gonka_compose__die "invalid GONKA_INCLUDE_DEVSHARD_PROXY=$include_proxy"
      return 1
      ;;
  esac

  COMPOSE_FILE_ARRAY=("${files[@]}")
  COMPOSE_FILES="${files[*]}"
  export COMPOSE_FILES COMPOSE_FILE_ARRAY
}

gonka_compose_define_dc() {
  dc() {
    docker compose "${COMPOSE_FILE_ARRAY[@]}" "$@"
  }
  export -f dc 2>/dev/null || true
}

gonka_compose_define_devshard_dc() {
  if [[ ! -f "${JOIN_DIR}/docker-compose.devshardctl.yml" ]]; then
    devshard_dc() {
      gonka_compose__die "missing ${JOIN_DIR}/docker-compose.devshardctl.yml"
      return 1
    }
    GONKA_DEVSHARD_COMPOSE_AVAILABLE=0
    return 0
  fi

  devshard_dc() {
    docker compose -f docker-compose.devshardctl.yml "$@"
  }
  GONKA_DEVSHARD_COMPOSE_AVAILABLE=1
  export -f devshard_dc 2>/dev/null || true
}

gonka_compose_show() {
  echo "JOIN_DIR=$JOIN_DIR"
  echo "CHAIN_ID=${GONKA_CHAIN_ID:-unknown}"
  echo "KEY_NAME=${GONKA_KEY_NAME:-<unset>}"
  echo "API_PORT=${GONKA_API_PORT:-8000}"
  echo "config.env=sourced"
  echo "config.devshard.env=${GONKA_DEVSHARD_ENV_SOURCED:-0}"
  echo ".env=${GONKA_DOTENV_SOURCED:-0}"
  echo "COMPOSE_FILES=$COMPOSE_FILES"
  echo
  echo "Compose files present:"
  local f
  for f in \
    docker-compose.yml \
    docker-compose.mlnode.yml \
    docker-compose.env-override.yml \
    docker-compose.genesis-override.yml \
    docker-compose.runtime-override.yml \
    docker-compose.devshard-proxy-override.yml \
    docker-compose.devshardctl.yml \
    docker-compose.postgres.yml \
    docker-compose.observability.yml; do
    if [[ -f "${JOIN_DIR}/${f}" ]]; then
      echo "  [yes] $f"
    else
      echo "  [ no] $f"
    fi
  done
  echo
  echo "Node/API restart (after prepare):"
  echo "  dc up -d --no-deps --force-recreate node"
  echo "  dc up -d --no-deps --force-recreate api"
  echo "  dc ps node api"
  if [[ "${GONKA_DEVSHARD_COMPOSE_AVAILABLE:-0}" == "1" ]]; then
    echo
    echo "Devshard gateway (separate stack):"
    echo "  devshard_dc pull devshardctl"
    echo "  devshard_dc up -d --no-deps --force-recreate devshardctl"
  fi
}

gonka_compose_prepare() {
  gonka_compose_resolve_join_dir || return 1
  gonka_compose_source_env_files || return 1
  gonka_compose_discover_files || return 1
  gonka_compose_define_dc
  gonka_compose_define_devshard_dc
  GONKA_COMPOSE_PREPARED=1
  export GONKA_COMPOSE_PREPARED
}
