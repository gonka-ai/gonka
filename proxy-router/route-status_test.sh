#!/bin/sh
set -eu

ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

mkdir "$TMPDIR/bin"
cat > "$TMPDIR/bin/socat" <<'EOF'
#!/bin/sh
set -eu
command=$(cat)
if [ "$command" = "${FAIL_COMMAND:-}" ]; then
    exit 1
fi
case "$command" in
    "show map /etc/haproxy/version-router.map")
        if [ "${INVALID_MAP_OUTPUT:-}" = true ]; then
            printf '%s\n' 'Unknown map identifier. Please use #<id> or <file>.'
        else
            printf '%s\n' '0x1 v5 versiond_routers_v5'
        fi
        ;;
    "show servers state versiond_routers_v5")
        if [ "${INVALID_SERVERS_OUTPUT:-}" = true ]; then
            printf '%s\n' 'Unknown backend name.'
        else
            printf '%s\n' '1' \
                '# be_id srv_addr be_name srv_id srv_name srv_op_state' \
                '1 192.0.2.10 2 3 router1 2'
        fi
        ;;
    "show servers state versiond_router_coarse")
        printf '%s\n' '1' \
            '# be_id srv_addr be_name srv_id srv_name srv_op_state' \
            '1 192.0.2.10 2 3 router1 2'
        ;;
    "show stat")
        printf '%s\n' '# pxname,svname,status'
        for backend in versiond_routers_v5 versiond_router_coarse; do
            awk -v backend="$backend" -v status="${STAT_STATUS:-UP}" 'BEGIN {
                printf "%s,router1", backend
                for (field = 3; field < 18; field++) printf ","
                print "," status
            }'
        done
        if [ -n "${H2_TWIN:-}" ]; then
            case "$H2_TWIN" in
                up) h2_state=UP ;;
                down) h2_state=DOWN ;;
                *) h2_state=$H2_TWIN ;;
            esac
            awk -v h2_state="$h2_state" 'BEGIN {
                printf "%s,router1", "versiond_routers_v5_rpc"
                for (field = 3; field < 18; field++) printf ","
                print "," h2_state
            }'
        fi
        ;;
    *)
        exit 1
        ;;
esac
EOF
chmod +x "$TMPDIR/bin/socat"

run_status() {
    PATH="$TMPDIR/bin:$PATH" HAPROXY_SOCKET=unused \
        sh "$ROOT/route-status" "$@"
}

run_status v5 192.0.2.10
run_status --coarse 192.0.2.10

if run_status v4 192.0.2.10 >/dev/null 2>&1; then
    echo "route-status accepted an unknown version" >&2
    exit 1
else
    status=$?
    [ "$status" -eq 3 ] || {
        echo "route-status returned $status instead of 3 for an unknown version" >&2
        exit 1
    }
fi

if run_status v5 192.0.2.11 >/dev/null 2>&1; then
    echo "route-status admitted an unknown address" >&2
    exit 1
fi

if STAT_STATUS=DOWN run_status v5 192.0.2.10 >/dev/null 2>&1; then
    echo "route-status admitted a server that show stat reports as DOWN" >&2
    exit 1
fi

for command in \
    "show map /etc/haproxy/version-router.map" \
    "show servers state versiond_routers_v5" \
    "show stat"; do
    if FAIL_COMMAND="$command" run_status v5 192.0.2.10 >/dev/null 2>&1; then
        echo "route-status accepted failed Runtime API command: $command" >&2
        exit 1
    else
        status=$?
        [ "$status" -ne 3 ] || {
            echo "route-status confused Runtime API failure with an unknown version" >&2
            exit 1
        }
    fi
done

if INVALID_MAP_OUTPUT=true run_status v4 192.0.2.10 >/dev/null 2>&1; then
    echo "route-status accepted an invalid show map response" >&2
    exit 1
else
    status=$?
    [ "$status" -eq 1 ] || {
        echo "route-status returned $status instead of 1 for invalid show map output" >&2
        exit 1
    }
fi

if INVALID_SERVERS_OUTPUT=true run_status v5 192.0.2.10 >/dev/null 2>&1; then
    echo "route-status accepted an invalid show servers state response" >&2
    exit 1
fi

# Prefix assignments on functions persist in this shell, so clear the
# failure injections from the cases above.
if ! H2_TWIN=up INVALID_MAP_OUTPUT= INVALID_SERVERS_OUTPUT= FAIL_COMMAND= STAT_STATUS= \
    run_status v5 192.0.2.10 >/dev/null 2>&1; then
    echo "route-status rejected a version whose peer RPC twin is UP" >&2
    exit 1
fi

if H2_TWIN=down INVALID_MAP_OUTPUT= INVALID_SERVERS_OUTPUT= FAIL_COMMAND= STAT_STATUS= \
    run_status v5 192.0.2.10 >/dev/null 2>&1; then
    echo "route-status admitted a version whose peer RPC twin is DOWN" >&2
    exit 1
fi

echo "route-status_test: ok"
