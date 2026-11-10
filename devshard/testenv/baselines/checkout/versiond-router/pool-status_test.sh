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
    "show stat")
        printf '%s\n' '# pxname,svname,status'
        awk 'BEGIN {
            printf "versiond_ha_pool,versiond-pool1"
            for (field = 3; field < 18; field++) printf ","
            print ",UP"
        }'
        ;;
    "show backend")
        printf '%s\n' '# name' 'versiond_ha_pool'
        ;;
    "show servers state versiond_ha_pool")
        printf '%s\n' '# header' '# fields' \
            '1 2 3 versiond-pool1 192.0.2.10 6 7 8'
        ;;
    *)
        exit 1
        ;;
esac
EOF
chmod +x "$TMPDIR/bin/socat"

PATH="$TMPDIR/bin:$PATH" HAPROXY_SOCKET=unused \
    "$ROOT/pool-status" > "$TMPDIR/status"
grep -q "versiond-pool1.*192.0.2.10.*UP" "$TMPDIR/status"
grep -q '1 server(s) taking traffic' "$TMPDIR/status"

for command in "show stat" "show backend" "show servers state versiond_ha_pool"; do
    if PATH="$TMPDIR/bin:$PATH" HAPROXY_SOCKET=unused FAIL_COMMAND="$command" \
        "$ROOT/pool-status" > /dev/null 2>&1; then
        echo "pool-status accepted failed Runtime API command: $command" >&2
        exit 1
    fi
done

echo "pool-status_test: ok"
