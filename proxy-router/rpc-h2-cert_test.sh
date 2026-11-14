#!/bin/sh
# Renewal replaces cert.pem and reloads nginx. The HAProxy PEM and the
# certificate it already loaded must follow.
set -eu

root=$(CDPATH='' cd -- "$(dirname "$0")" && pwd)
# shellcheck disable=SC1091
. "$root/rpc-h2-cert.sh"

fail() {
    echo "rpc-h2-cert: $1" >&2
    exit 1
}

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

mint() {
    openssl req -x509 -newkey rsa:2048 -keyout "$2" -out "$1" \
        -days 1 -nodes -subj "/CN=$3" >/dev/null 2>&1 \
        || fail "could not mint $3"
}

mint "$tmpdir/cert.pem" "$tmpdir/private.key" initial.test
write_rpc_h2_pem "$tmpdir/cert.pem" "$tmpdir/private.key" "$tmpdir/rpc.pem"
grep -q 'BEGIN CERTIFICATE' "$tmpdir/rpc.pem" || fail "PEM is missing the certificate"
grep -q 'PRIVATE KEY' "$tmpdir/rpc.pem" || fail "PEM is missing the private key"
mode=$(ls -l "$tmpdir/rpc.pem" | awk '{print $1}')
case "$mode" in
    -rw-------*) ;;
    *) fail "PEM mode is $mode" ;;
esac

python3 - <<PY
import socket
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.bind("$tmpdir/admin.sock")
PY
[ -S "$tmpdir/admin.sock" ] || fail "admin socket was not created"

mkdir -p "$tmpdir/bin"
cat > "$tmpdir/bin/socat" <<'EOF'
#!/bin/sh
if [ "${RPC_H2_SOCAT_FAIL:-}" = 1 ]; then
    echo "Permission denied" >&2
    exit 1
fi
cat >> "${RPC_H2_SOCAT_LOG:?}"
exit 0
EOF
chmod +x "$tmpdir/bin/socat"
PATH="$tmpdir/bin:$PATH"
export PATH
export RPC_H2_SOCAT_LOG="$tmpdir/socat.log"

seen=$(rpc_h2_bundle_hash "$tmpdir/cert.pem" "$tmpdir/private.key")
again=$(reload_rpc_h2_cert "$tmpdir/cert.pem" "$tmpdir/private.key" \
    "$tmpdir/rpc.pem" "$tmpdir/admin.sock" "$seen")
[ "$again" = "$seen" ] || fail "unchanged bundle was reloaded"
[ ! -e "$tmpdir/socat.log" ] || fail "unchanged bundle talked to HAProxy"

mint "$tmpdir/cert.pem" "$tmpdir/next.key" renewed.test
# nginx renewal replaces the certificate and leaves the private key.
next=$(reload_rpc_h2_cert "$tmpdir/cert.pem" "$tmpdir/private.key" \
    "$tmpdir/rpc.pem" "$tmpdir/admin.sock" "$seen")
[ "$next" != "$seen" ] || fail "renewed bundle kept the old hash"
grep -q 'BEGIN CERTIFICATE' "$tmpdir/rpc.pem" || fail "renewed PEM dropped the certificate"
grep -q "set ssl cert $tmpdir/rpc.pem <<%EOF%" "$tmpdir/socat.log" \
    || fail "reload did not set the bound PEM"
openssl x509 -in "$tmpdir/cert.pem" -noout -subject | grep -q renewed.test \
    || fail "renewed cert was not written"
openssl x509 -in "$tmpdir/rpc.pem" -noout -subject | grep -q renewed.test \
    || fail "HAProxy PEM still has the initial certificate"
grep -q "commit ssl cert $tmpdir/rpc.pem" "$tmpdir/socat.log" \
    || fail "reload did not commit the certificate"
grep -q '%EOF%' "$tmpdir/socat.log" || fail "PEM payload has no terminator"

export RPC_H2_SOCAT_FAIL=1
mint "$tmpdir/cert.pem" "$tmpdir/third.key" failed.test
: > "$tmpdir/socat.log"
kept=$(reload_rpc_h2_cert "$tmpdir/cert.pem" "$tmpdir/private.key" \
    "$tmpdir/rpc.pem" "$tmpdir/admin.sock" "$next")
[ "$kept" = "$next" ] || fail "a failed load advanced the remembered hash"
unset RPC_H2_SOCAT_FAIL
: > "$tmpdir/socat.log"
retried=$(reload_rpc_h2_cert "$tmpdir/cert.pem" "$tmpdir/private.key" \
    "$tmpdir/rpc.pem" "$tmpdir/admin.sock" "$kept")
[ "$retried" != "$kept" ] || fail "the failed renewal was not retried"
grep -q "commit ssl cert $tmpdir/rpc.pem" "$tmpdir/socat.log" \
    || fail "retry did not commit the certificate"

echo "rpc-h2-cert: ok"
