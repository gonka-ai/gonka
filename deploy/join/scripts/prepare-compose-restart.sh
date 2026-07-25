#!/usr/bin/env bash
# Source env files and discover docker compose overrides for node/api restarts.
#
# Run ON a join host (or from repo with GONKA_DEPLOY_DIR set).
# Does not restart services by default — use prepare/show first, then dc manually.
#
# Usage:
#   ./scripts/prepare-compose-restart.sh prepare     # source env + export dc()
#   ./scripts/prepare-compose-restart.sh show        # print resolved compose stack
#   ./scripts/prepare-compose-restart.sh list-hosts  # print known hosts (from laptop)
#   ./scripts/prepare-compose-restart.sh list-hosts testnet-4
#   ./scripts/prepare-compose-restart.sh halt api 20          # stop service, wait, start (this host)
#   ./scripts/prepare-compose-restart.sh coordinated-halt node 30 testnet-4  # stop, wait, start
#   ./scripts/prepare-compose-restart.sh coordinated-stop node testnet-4
#   ./scripts/prepare-compose-restart.sh coordinated-start node testnet-4
#
# After prepare (same shell):
#   dc ps node api
#   dc up -d --no-deps --force-recreate node
#   dc up -d --no-deps --force-recreate api
#
# Optional env:
#   GONKA_DEPLOY_DIR          — override deploy/join path
#   GONKA_SKIP_DEVSHARD_ENV=1 — skip config.devshard.env
#   GONKA_HOSTS_FILE          — override hosts.inventory path
#   GONKA_HOSTS_FILTER        — grep filter for list-hosts (e.g. testnet-4, 702111)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
JOIN_DIR_DEFAULT="$(cd "$SCRIPT_DIR/.." && pwd)"
LIB="${SCRIPT_DIR}/lib/compose-env.sh"
HOSTS_FILE="${GONKA_HOSTS_FILE:-${JOIN_DIR_DEFAULT}/hosts.inventory}"

log() { echo "[prepare-compose] $*"; }
die() { echo "[prepare-compose] ERROR: $*" >&2; exit 1; }

usage() {
  sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}

list_hosts() {
  local filter="${1:-${GONKA_HOSTS_FILTER:-}}"
  [[ -f "$HOSTS_FILE" ]] || die "hosts file not found: $HOSTS_FILE"

  echo "Gonka join hosts ($HOSTS_FILE)"
  echo
  printf "%-14s %-10s %-28s %-6s %-6s %-36s %-18s %s\n" \
    "ID" "ROLE" "SSH" "PORT" "API" "DEPLOY_DIR" "CHAIN" "NOTES"
  printf "%-14s %-10s %-28s %-6s %-6s %-36s %-18s %s\n" \
    "----" "----" "---" "----" "---" "----------" "-----" "-----"

  while IFS='|' read -r id role user host port api deploy chain notes; do
    [[ -z "${id:-}" || "$id" =~ ^# ]] && continue
    [[ -n "$filter" ]] && ! echo "$id|$role|$chain|$notes" | grep -qi "$filter" && continue
    local ssh_label="${user}@${host}"
    printf "%-14s %-10s %-28s %-6s %-6s %-36s %-18s %s\n" \
      "$id" "$role" "$ssh_label" "$port" "$api" "$deploy" "$chain" "${notes:-}"
  done < "$HOSTS_FILE"

  echo
  echo "SSH examples (testnet-4):"
  echo "  ssh -p 18222 decentai@xj7-5.s.filfox.io   # 702111 genesis"
  echo "  ssh -p 18221 decentai@xj7-5.s.filfox.io   # 702112"
  echo "  ssh -p 18223 decentai@xj7-5.s.filfox.io   # 702127"
  echo "  ssh -p 18226 decentai@xj7-5.s.filfox.io   # 702105"
  echo
  echo "On each host:"
  echo "  cd /srv/dai/gonka/deploy/join"
  echo "  ./scripts/prepare-compose-restart.sh prepare"
}

cmd_prepare() {
  [[ -f "$LIB" ]] || die "missing library: $LIB"
  export JOIN_DIR="${GONKA_DEPLOY_DIR:-$JOIN_DIR_DEFAULT}"
  # shellcheck disable=SC1090
  source "$LIB"
  gonka_compose_prepare || exit 1
  log "ready — JOIN_DIR=$JOIN_DIR"
  log "COMPOSE_FILES=$COMPOSE_FILES"
  log "Use: dc ps node api"
  log "Use: dc up -d --no-deps --force-recreate node|api"
}

cmd_show() {
  [[ -f "$LIB" ]] || die "missing library: $LIB"
  export JOIN_DIR="${GONKA_DEPLOY_DIR:-$JOIN_DIR_DEFAULT}"
  # shellcheck disable=SC1090
  source "$LIB"
  gonka_compose_prepare || exit 1
  gonka_compose_show
}

cmd_halt() {
  local service="${1:?service name required (e.g. node, api)}"
  local seconds="${2:?seconds required (e.g. 20)}"
  [[ "$seconds" =~ ^[0-9]+$ ]] || die "seconds must be a positive integer"

  [[ -f "$LIB" ]] || die "missing library: $LIB"
  export JOIN_DIR="${GONKA_DEPLOY_DIR:-$JOIN_DIR_DEFAULT}"
  # shellcheck disable=SC1090
  source "$LIB"
  gonka_compose_prepare || exit 1

  log "stopping $service..."
  dc stop "$service"
  local container_status
  container_status="$(docker inspect "$service" --format '{{.State.Status}}' 2>/dev/null || echo missing)"
  log "$service down (status=$container_status)"
  log "waiting ${seconds}s..."
  sleep "$seconds"
  log "starting $service..."
  dc start "$service"
  sleep 2
  container_status="$(docker inspect "$service" --format '{{.State.Status}}' 2>/dev/null || echo missing)"
  log "$service up (status=$container_status)"
  dc ps "$service" --format 'table {{.Name}}\t{{.Status}}'
}

remote_halt_script() {
  local service="$1"
  local seconds="$2"
  cat <<REMOTE
set -euo pipefail
cd "\${DEPLOY_DIR}"
source ./scripts/prepare-compose-restart.sh prepare >/dev/null
dc stop ${service}
docker inspect ${service} --format '{{.State.Status}}' 2>/dev/null || echo missing
REMOTE
}

remote_start_script() {
  local service="$1"
  cat <<REMOTE
set -euo pipefail
cd "\${DEPLOY_DIR}"
source ./scripts/prepare-compose-restart.sh prepare >/dev/null
dc start ${service}
sleep 2
docker inspect ${service} --format '{{.State.Status}}' 2>/dev/null || echo missing
REMOTE
}

_fleet_load_hosts() {
  local filter="${1:-testnet-4}"
  FLEET_IDS=()
  FLEET_USERS=()
  FLEET_HOSTS=()
  FLEET_PORTS=()
  FLEET_DEPLOYS=()
  while IFS='|' read -r id role user host port api deploy chain notes; do
    [[ -z "${id:-}" || "$id" =~ ^# ]] && continue
    [[ -n "$filter" ]] && ! echo "$id|$role|$chain|$notes" | grep -qi "$filter" && continue
    FLEET_IDS+=("$id")
    FLEET_USERS+=("$user")
    FLEET_HOSTS+=("$host")
    FLEET_PORTS+=("$port")
    FLEET_DEPLOYS+=("$deploy")
  done < "$HOSTS_FILE"
  ((${#FLEET_IDS[@]} > 0)) || die "no hosts matched filter: $filter"
}

_fleet_docker_stop() {
  local -a services=("$@")
  local svc_label i ssh_target stop_failed=0
  svc_label="$(echo "${services[*]}" | tr ' ' '_' | tr '[:lower:]' '[:upper:]')"
  log "=== stopping ${services[*]} on all hosts (docker stop) ==="
  for i in "${!FLEET_IDS[@]}"; do
    ssh_target="${FLEET_USERS[$i]}@${FLEET_HOSTS[$i]}"
    (
      ssh -p "${FLEET_PORTS[$i]}" -o BatchMode=yes -o ConnectTimeout=60 -o ServerAliveInterval=10 "$ssh_target" \
        "SERVICES='${services[*]}' bash -s" <<'REMOTE'
set -euo pipefail
for svc in $SERVICES; do
  if docker inspect "$svc" >/dev/null 2>&1; then
    docker stop -t 5 "$svc" >/dev/null 2>&1 || true
  else
    echo "WARN: container ${svc} not found" >&2
  fi
done
sleep 1
for svc in $SERVICES; do
  st="$(docker inspect "$svc" --format '{{.State.Status}}' 2>/dev/null || echo missing)"
  echo "${svc}=${st}"
  if [[ "$st" == "running" ]]; then
    echo "ERROR: ${svc} still running after docker stop" >&2
    exit 1
  fi
done
REMOTE
      rc=$?
      if [[ "$rc" -eq 0 ]]; then
        echo "${FLEET_IDS[$i]}: stopped"
      else
        echo "${FLEET_IDS[$i]}: stop command exited $rc (will verify)" >&2
      fi
      exit 0
    ) &
  done
  wait
  log "=== verify down ==="
  for i in "${!FLEET_IDS[@]}"; do
    ssh_target="${FLEET_USERS[$i]}@${FLEET_HOSTS[$i]}"
    local status_line="" any_running=0 st
    for svc in "${services[@]}"; do
      st="$(ssh -p "${FLEET_PORTS[$i]}" -o BatchMode=yes "$ssh_target" \
        "docker inspect ${svc} --format '{{.State.Status}}' 2>/dev/null || echo missing")"
      status_line+="${svc}=${st} "
      [[ "$st" == "running" ]] && any_running=1
    done
    echo "${FLEET_IDS[$i]}: ${status_line}"
    [[ "$any_running" -eq 0 ]] || stop_failed=1
  done
  [[ "$stop_failed" -eq 0 ]] || die "verify down failed — service(s) still running"
  log "ALL_${svc_label}_DOWN at $(date -u +%Y-%m-%dT%H:%M:%SZ)"
}

_fleet_docker_start() {
  local -a services=("$@")
  local svc_label i ssh_target start_failed=0
  svc_label="$(echo "${services[*]}" | tr ' ' '_' | tr '[:lower:]' '[:upper:]')"
  log "=== starting ${services[*]} on all hosts (docker start) ==="
  for i in "${!FLEET_IDS[@]}"; do
    ssh_target="${FLEET_USERS[$i]}@${FLEET_HOSTS[$i]}"
    (
      ssh -p "${FLEET_PORTS[$i]}" -o BatchMode=yes -o ConnectTimeout=60 -o ServerAliveInterval=10 "$ssh_target" \
        "SERVICES='${services[*]}' bash -s" <<'REMOTE'
set -euo pipefail
for svc in $SERVICES; do
  if docker inspect "$svc" >/dev/null 2>&1; then
    docker start "$svc" >/dev/null 2>&1 || true
  else
    echo "WARN: container ${svc} not found" >&2
  fi
done
sleep 2
for svc in $SERVICES; do
  st="$(docker inspect "$svc" --format '{{.State.Status}}' 2>/dev/null || echo missing)"
  echo "${svc}=${st}"
  if [[ "$st" != "running" ]]; then
    echo "ERROR: ${svc} not running after docker start (status=${st})" >&2
    exit 1
  fi
done
REMOTE
      rc=$?
      if [[ "$rc" -eq 0 ]]; then
        echo "${FLEET_IDS[$i]}: started"
      else
        echo "${FLEET_IDS[$i]}: start command exited $rc (will verify)" >&2
      fi
      exit 0
    ) &
  done
  wait
  log "=== verify up ==="
  for i in "${!FLEET_IDS[@]}"; do
    ssh_target="${FLEET_USERS[$i]}@${FLEET_HOSTS[$i]}"
    local status_line="" any_down=0 st
    for svc in "${services[@]}"; do
      st="$(ssh -p "${FLEET_PORTS[$i]}" -o BatchMode=yes "$ssh_target" \
        "docker inspect ${svc} --format '{{.State.Status}}' 2>/dev/null || echo missing")"
      status_line+="${svc}=${st} "
      [[ "$st" != "running" ]] && any_down=1
    done
    echo "${FLEET_IDS[$i]}: ${status_line}"
    [[ "$any_down" -eq 0 ]] || start_failed=1
  done
  [[ "$start_failed" -eq 0 ]] || die "verify up failed — service(s) not running"
  log "ALL_${svc_label}_STARTED at $(date -u +%Y-%m-%dT%H:%M:%SZ)"
}

cmd_coordinated_stop() {
  local services_raw="${1:?service name(s) required (e.g. node)}"
  local filter="${2:-testnet-4}"
  services_raw="${services_raw//,/ }"
  read -r -a services <<< "$services_raw"
  ((${#services[@]} > 0)) || die "no services specified"
  [[ -f "$HOSTS_FILE" ]] || die "hosts file not found: $HOSTS_FILE"
  _fleet_load_hosts "$filter"
  log "coordinated stop: services=${services[*]} hosts=${FLEET_IDS[*]}"
  _fleet_docker_stop "${services[@]}"
}

cmd_coordinated_start() {
  local services_raw="${1:?service name(s) required (e.g. node)}"
  local filter="${2:-testnet-4}"
  services_raw="${services_raw//,/ }"
  read -r -a services <<< "$services_raw"
  ((${#services[@]} > 0)) || die "no services specified"
  [[ -f "$HOSTS_FILE" ]] || die "hosts file not found: $HOSTS_FILE"
  _fleet_load_hosts "$filter"
  log "coordinated start: services=${services[*]} hosts=${FLEET_IDS[*]}"
  _fleet_docker_start "${services[@]}"
}

cmd_coordinated_halt() {
  local services_raw="${1:?service name(s) required (e.g. node, api, or node,api)}"
  local seconds="${2:?seconds required (e.g. 20)}"
  local filter="${3:-testnet-4}"
  services_raw="${services_raw//,/ }"
  read -r -a services <<< "$services_raw"
  ((${#services[@]} > 0)) || die "no services specified"
  [[ "$seconds" =~ ^[0-9]+$ ]] || die "seconds must be a positive integer"
  [[ -f "$HOSTS_FILE" ]] || die "hosts file not found: $HOSTS_FILE"
  _fleet_load_hosts "$filter"

  local svc_label
  svc_label="$(echo "${services[*]}" | tr ' ' '_' | tr '[:lower:]' '[:upper:]')"
  log "coordinated halt: services=${services[*]} seconds=$seconds hosts=${FLEET_IDS[*]}"
  _fleet_docker_stop "${services[@]}"
  log "waiting ${seconds}s..."
  sleep "$seconds"
  _fleet_docker_start "${services[@]}"
}

main() {
  local cmd="${1:-prepare}"
  shift || true

  case "$cmd" in
    -h|--help|help)
      usage 0
      ;;
    list-hosts|hosts|list)
      list_hosts "${1:-}"
      ;;
    prepare|source|env)
      cmd_prepare
      ;;
    show|print|status)
      cmd_show
      ;;
    halt|service-halt)
      cmd_halt "$@"
      ;;
    coordinated-halt|fleet-halt)
      cmd_coordinated_halt "$@"
      ;;
    coordinated-stop|fleet-stop)
      cmd_coordinated_stop "$@"
      ;;
    coordinated-start|fleet-start)
      cmd_coordinated_start "$@"
      ;;
    *)
      die "unknown command: $cmd (try: prepare | show | list-hosts | halt | coordinated-halt | coordinated-stop | coordinated-start)"
      ;;
  esac
}

# When sourced, run subcommand if arguments were passed.
if [[ "${BASH_SOURCE[0]}" != "${0}" ]]; then
  [[ $# -gt 0 ]] && main "$@"
else
  main "$@"
fi
