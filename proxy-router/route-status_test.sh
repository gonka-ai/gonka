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
        printf '%s\n' '0x1 v5 versiond_routers_v5'
        ;;
    "show servers state versiond_routers_v5")
        printf '%s\n' '# header' '# fields' \
            '1 2 3 router1 192.0.2.10 2 0 8'
        ;;
    "show servers state versiond_router_coarse")
        printf '%s\n' '# header' '# fields' \
            '1 2 3 router1 192.0.2.10 2 0 8'
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

for command in \
    "show map /etc/haproxy/version-router.map" \
    "show servers state versiond_routers_v5"; do
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

echo "route-status_test: ok"
