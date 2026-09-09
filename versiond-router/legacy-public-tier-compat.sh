#!/bin/sh

set -eu

out=${VERSIOND_ROUTER_OUT:-/etc/nginx/conf.d/default.conf}
bind_host=${VERSIOND_ROUTER_FRONT_BIND_HOST:-}

case "$(printf '%s' "${VERSIOND_ROUTER_TRUST_FORWARDED_HEADERS:-}" \
    | sed 's/^[[:space:]]*//; s/[[:space:]]*$//' | tr '[:upper:]' '[:lower:]')" in
    1 | t | true | yes | on) trust_forwarded=1 ;;
    '' | 0 | f | false | no | off) trust_forwarded= ;;
    *)
        echo "versiond-router: invalid VERSIOND_ROUTER_TRUST_FORWARDED_HEADERS" >&2
        exit 1
        ;;
esac

if [ -z "$bind_host" ]; then
    if [ -n "$trust_forwarded" ]; then
        echo "versiond-router: trusted forwarded headers require an isolated bind host" >&2
        exit 1
    fi
    exit 0
fi

bind_address=
for candidate in $(getent ahostsv4 "$bind_host" | awk '!seen[$1]++ { print $1 }'); do
    if ip -o -4 addr show | awk -v candidate="$candidate" '
        { address = $4; sub(/\/.*/, "", address); if (address == candidate) found = 1 }
        END { exit !found }
    '; then
        bind_address=$candidate
        break
    fi
done
[ -n "$bind_address" ] || {
    echo "versiond-router: cannot resolve local address for '$bind_host'" >&2
    exit 1
}
[ -f "$out" ] || {
    echo "versiond-router: legacy nginx config '$out' was not rendered" >&2
    exit 1
}
grep -Eq 'listen[[:space:]]+8080;' "$out" || {
    echo "versiond-router: legacy nginx listener contract changed" >&2
    exit 1
}

if [ -n "$trust_forwarded" ]; then
    if ! grep -Fq "proxy_set_header X-Real-IP \$remote_addr;" "$out" \
        || ! grep -Fq "proxy_set_header X-Forwarded-Proto \$scheme;" "$out"; then
        echo "versiond-router: legacy forwarded-header contract changed" >&2
        exit 1
    fi
    # nginx variables must stay literal in these substitutions.
    # shellcheck disable=SC2016
    sed -i \
        -e "s/listen[[:space:]][[:space:]]*8080;/listen ${bind_address}:8080;/" \
        -e 's/proxy_set_header X-Real-IP \$remote_addr;/proxy_set_header X-Real-IP \$http_x_real_ip;/' \
        -e 's/proxy_set_header X-Forwarded-Proto \$scheme;/proxy_set_header X-Forwarded-Proto \$http_x_forwarded_proto;/' \
        "$out"
else
    sed -i \
        -e "s/listen[[:space:]][[:space:]]*8080;/listen ${bind_address}:8080;/" \
        "$out"
fi

printf '%s\n' "$bind_address" > /var/run/versiond-router-listen-address
