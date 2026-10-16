#!/bin/sh
set -eu
# The cluster DNS address differs between providers and may be NodeLocal DNS.
# HAProxy does not apply resolv.conf search suffixes: chart targets use FQDNs.
dns=$(awk '$1 == "nameserver" { print $2; exit }' /etc/resolv.conf)
[ -n "$dns" ] || { echo 'No nameserver in /etc/resolv.conf' >&2; exit 1; }
case "$dns" in
    *:*) haproxy_dns="[$dns]:53"; nginx_dns="[$dns]" ;;
    *) haproxy_dns="$dns:53"; nginx_dns="$dns" ;;
esac
export HAPROXY_DNS_RESOLVER="${HAPROXY_DNS_RESOLVER:-$haproxy_dns}"
export RESOLVER="${RESOLVER:-$nginx_dns}"
exec "$@"
