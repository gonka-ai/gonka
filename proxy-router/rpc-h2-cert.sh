# Rebuild the peer-RPC PEM from nginx's cert.pem and private.key, then commit
# it on the HAProxy admin socket. nginx renewal replaces those two files and
# reloads nginx; it does not touch the PEM HAProxy loaded at start.

rpc_h2_bundle_hash() {
    cert=$1
    key=$2
    if [ ! -f "$cert" ] || [ ! -f "$key" ]; then
        printf '%s\n' missing
        return 0
    fi
    if command -v sha256sum >/dev/null 2>&1; then
        cat "$cert" "$key" | sha256sum | awk '{print $1}'
    else
        cat "$cert" "$key" | shasum -a 256 | awk '{print $1}'
    fi
}

write_rpc_h2_pem() {
    cert=$1
    key=$2
    dest=$3
    tmp="${dest}.tmp"
    mkdir -p "$(dirname "$dest")"
    cat "$cert" "$key" > "$tmp"
    chmod 600 "$tmp"
    mv -f "$tmp" "$dest"
}

# load_rpc_h2_cert sends the PEM already written at $2. The path must be the
# same crt path HAProxy bound, or commit does not replace the live certificate.
load_rpc_h2_cert() {
    sock=$1
    pem=$2
    payload=$(sed -n '/^$/d;/-BEGIN/,/-END/p' "$pem")
    if [ -z "$payload" ]; then
        echo "proxy-router: peer RPC PEM has no certificate" >&2
        return 1
    fi
    set_out=$(
        {
            printf 'set ssl cert %s <<%%EOF%%\n' "$pem"
            printf '%s\n' "$payload"
            printf '%%EOF%%\n'
        } | socat -t 10 stdio "$sock"
    ) || set_out="socat failed: ${set_out}"
    case "$set_out" in
        *rror*|*nknown*|*Permission*|"socat failed"*)
            printf 'abort ssl cert %s\n' "$pem" | socat -t 5 stdio "$sock" >/dev/null 2>&1 || true
            echo "proxy-router: set ssl cert failed: $set_out" >&2
            return 1
            ;;
    esac
    commit_out=$(printf 'commit ssl cert %s\n' "$pem" | socat -t 10 stdio "$sock") \
        || commit_out="socat failed: ${commit_out}"
    case "$commit_out" in
        *rror*|*nknown*|*Permission*|"socat failed"*)
            echo "proxy-router: commit ssl cert failed: $commit_out" >&2
            return 1
            ;;
    esac
}

# Prints the hash to remember. A failed load keeps the previous hash so the
# next pass retries the same bundle.
reload_rpc_h2_cert() {
    cert=$1
    key=$2
    pem=$3
    sock=$4
    seen=$5
    next=$(rpc_h2_bundle_hash "$cert" "$key")
    if [ "$next" = "$seen" ] || [ "$next" = missing ]; then
        printf '%s\n' "$seen"
        return 0
    fi
    if ! write_rpc_h2_pem "$cert" "$key" "$pem"; then
        printf '%s\n' "$seen"
        return 0
    fi
    if [ ! -S "$sock" ]; then
        echo "proxy-router: peer RPC certificate changed; $sock is not ready" >&2
        printf '%s\n' "$seen"
        return 0
    fi
    if load_rpc_h2_cert "$sock" "$pem"; then
        echo "proxy-router: loaded renewed certificate for DEVSHARD_RPC_H2_PORT" >&2
        printf '%s\n' "$next"
        return 0
    fi
    echo "proxy-router: failed to load renewed peer RPC certificate" >&2
    printf '%s\n' "$seen"
}

watch_rpc_h2_cert() {
    cert="${RPC_H2_CERT_DIR}/cert.pem"
    key="${RPC_H2_CERT_DIR}/private.key"
    sock="${PROXY_ROUTER_ADMIN_SOCKET:-/var/run/haproxy/reconciler.sock}"
    interval="${DEVSHARD_RPC_H2_CERT_POLL_SECONDS:-30}"
    seen=$(rpc_h2_bundle_hash "$cert" "$key")
    while [ ! -S "$sock" ]; do
        sleep 1
    done
    while :; do
        seen=$(reload_rpc_h2_cert "$cert" "$key" "$RPC_H2_PEM" "$sock" "$seen")
        sleep "$interval"
    done
}
