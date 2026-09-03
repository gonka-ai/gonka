#!/usr/bin/env bash

# Every host name one service expects to resolve must be published, on a
# network that service is attached to, by a service or alias of the rendered
# join model or of a router fleet slot. Networks are matched by their rendered
# name, so the external fleet networks join the two models. This is the render-
# time counterpart of the runtime cutover test: a renamed alias, a service
# moved off a network, or a new consumer of a name that nothing provides fails
# here in seconds.

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)

fail() {
    echo "compose-contract_test: $*" >&2
    exit 1
}

export DEVSHARD_POSTGRES_PASSWORD=test
export VERSIOND_ROUTER_METRICS_NETWORK=join_default
export GONKA_PRIVATE_BIND_IP=10.20.0.11
export NETWORK_NODE_PRIVATE_IP=10.20.0.11
export VERSIOND_BIND_IP=10.20.0.12
export KEY_NAME=test

render_main() {
    docker compose --project-directory "$script_dir" "$@" config --format json 2>/dev/null
}

render_slot() {
    VERSIOND_ROUTER_SLOT=0 docker compose --project-directory "$script_dir" \
        --project-name gonka-versiond-router-0 \
        -f "$script_dir/versiond-router-slot/docker-compose.yml" config --format json 2>/dev/null
}

# Which environment keys name a host another service must publish.
host_keys='["PROXY_PROTOCOL_PEER","PROXY_POLICY_READINESS_HOST","PROXY_POLICY_POOL_HOST",
  "PROXY_ROUTER_POLICY_BIND_HOST","PROXY_ROUTER_METRICS_BIND_HOST","PROXY_ROUTER_CATALOG_BIND_HOST",
  "PROXY_ROUTER_CATALOG_UPSTREAM_HOST","VERSIOND_ROUTER_POOL_HOST","VERSIOND_POOL_HOST",
  "VERSIOND_LEGACY_HOST","VERSIOND_SERVICE_NAME","EDGE_API_SERVICE_NAME","PGHOST","NODE_HOST",
  "VERSIOND_ROUTER_FRONT_BIND_HOST","VERSIOND_ROUTER_METRICS_BIND_HOST"]'
# Keys whose value is a URL rather than a bare host.
url_keys='["VERSIOND_ORACLE_URL","VERSIOND_ROUTING_CATALOG_URL","NODE_MANAGER_ADDR","CHAIN_GRPC_URL","CHAIN_RPC_URL"]'

# From one rendered model, emits "member\tservice\tnetwork-name" for every
# network a service joins, "provider\tnetwork-name\tname" for every name it
# publishes there (its service name and aliases) and "consumer\tservice\thost"
# for every host its environment expects to resolve. Services of the two
# models are told apart by the model label, so a fleet slot's "router" cannot
# collide with a main service.
project_edges() {
    local label=$1 model=$2
    jq -r --arg label "$label" --argjson host_keys "$host_keys" --argjson url_keys "$url_keys" '
      def netname($n): (.networks[$n].name // $n);
      def hostof($v): ($v | sub("^[a-z]+://"; "") | sub("[:/].*$"; ""));
      . as $m
      | ($m.services | to_entries[]) as $s
      | ($label + "/" + $s.key) as $service
      | (
          (($s.value.networks // {"default": {}} | to_entries[]) as $net
            | (netname($net.key)) as $netname
            | ("member\t\($service)\t\($netname)",
               "provider\t\($netname)\t\($s.key)",
               ("provider\t\($netname)\t" + (($net.value.aliases // [])[])))),
          (($s.value.environment // {}) | to_entries[]
            | select(.value != null and (.value | tostring) != "")
            | .key as $key
            | if ($host_keys | index($key)) then "consumer\t\($service)\t\(.value | tostring)"
              elif ($url_keys | index($key)) then "consumer\t\($service)\t" + hostof(.value | tostring)
              else empty end)
        )
    ' <<<"$model"
}

# check_model LABEL MODEL-LABEL MODEL [MODEL-LABEL MODEL...]: every host a
# service consumes must be provided on at least one network that service is
# attached to.
check_model() {
    local label=$1
    shift
    local -a edges=() missing=()
    local -A provided=() networks=()
    local kind service network host found=0 resolved
    while (($# > 0)); do
        mapfile -t -O "${#edges[@]}" edges < <(project_edges "$1" "$2")
        shift 2
    done
    for edge in "${edges[@]}"; do
        IFS=$'\t' read -r kind service host <<<"$edge"
        case $kind in
            provider) provided[$service/$host]=1 ;;   # service holds the network name here
            member) networks[$service]+="$host " ;;
        esac
    done
    for edge in "${edges[@]}"; do
        IFS=$'\t' read -r kind service host <<<"$edge"
        [[ $kind == consumer ]] || continue
        # Names resolved outside Docker DNS: loopback, literal addresses, "host".
        case $host in
            127.* | localhost | host | [0-9]*.[0-9]*.[0-9]*.[0-9]*) continue ;;
        esac
        resolved=false
        for network in ${networks[$service]-}; do
            [[ -z ${provided[$network/$host]-} ]] || resolved=true
        done
        if [[ $resolved == true ]]; then
            found=$((found + 1))
        else
            missing+=("$host for $service (networks: ${networks[$service]-none})")
        fi
    done
    ((${#missing[@]} == 0)) || fail "$label: no service publishes $(printf '%s; ' "${missing[@]}")"
    ((found > 0)) || fail "$label: no host contract was checked"
    echo "compose-contract_test: $label ok ($found host references resolve)"
}

main_single=$(render_main -f "$script_dir/docker-compose.yml") || fail "single model does not render"
main_ha=$(render_main -f "$script_dir/docker-compose.yml" -f "$script_dir/docker-compose.versiond.yml") || \
    fail "HA model does not render"
main_ha3=$(render_main -f "$script_dir/docker-compose.yml" -f "$script_dir/docker-compose.versiond.yml" \
    -f "$script_dir/docker-compose.versiond3.yml") || fail "three-replica model does not render"
main_private=$(render_main -f "$script_dir/docker-compose.yml" -f "$script_dir/docker-compose.versiond.yml" \
    -f "$script_dir/docker-compose.private-endpoints.yml") || fail "private-endpoints model does not render"
slot=$(render_slot) || fail "router slot does not render"

check_model "single" main "$main_single"
check_model "ha + fleet slot" main "$main_ha" slot "$slot"
check_model "three replicas + fleet slot" main "$main_ha3" slot "$slot"
check_model "private endpoints + fleet slot" main "$main_private" slot "$slot"

# The relationships the cutover depends on, spelled out so a refactor of the
# generic check cannot silently stop covering them.
jq -e '.services.proxy.networks["proxy-policy-front"].aliases | index("proxy-policy-ingress")' <<<"$main_single" >/dev/null || \
    fail "the public proxy no longer publishes proxy-policy-ingress for the policy workers"
jq -e '.services.versiond.networks["versiond-router-back"].aliases | index("versiond-pool")' <<<"$main_ha" >/dev/null || \
    fail "versiond no longer joins the router back network as versiond-pool"
jq -e '.services.router.networks.front.aliases | index("versiond-router-fleet")' <<<"$slot" >/dev/null || \
    fail "router slots no longer publish versiond-router-fleet for the public proxy"
jq -e '.services.proxy.networks["versiond-router-back"].aliases | index("versiond-routing-oracle")' <<<"$main_ha" >/dev/null || \
    fail "the public proxy no longer publishes versiond-routing-oracle for the router slots"
[[ $(jq -r '.networks["versiond-router-back"].name' <<<"$main_ha") == $(jq -r '.networks.back.name' <<<"$slot") ]] || \
    fail "the main model and the router slots disagree on the back network name"
[[ $(jq -r '.networks["versiond-router-front"].name' <<<"$main_ha") == $(jq -r '.networks.front.name' <<<"$slot") ]] || \
    fail "the main model and the router slots disagree on the front network name"

echo "compose-contract_test: ok"
