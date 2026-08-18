#!/bin/sh

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd -P)
hook=$script_dir/legacy-router-upgrade-barrier.sh
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

fail() {
    echo "legacy-router-upgrade-barrier_test: $*" >&2
    exit 1
}

renderer=$tmpdir/40-render-versiond-upstream.sh
cat >"$renderer" <<'EOF'
#!/bin/sh
printf '%s\n' "${VERSIOND_HOSTS-}" >"$RENDERED_HOSTS"
EOF
chmod +x "$renderer"

# No state is a normal start outside an upgrade barrier.
GONKA_UPGRADE_BARRIER_STATE="$tmpdir/missing" "$hook"

cat >"$tmpdir/state" <<EOF
VERSIOND_HOSTS
versiond
$renderer
EOF
RENDERED_HOSTS=$tmpdir/rendered \
GONKA_UPGRADE_BARRIER_STATE=$tmpdir/state \
    "$hook"
[ "$(cat "$tmpdir/rendered")" = versiond ] || fail \
    "the persisted host list was not rendered"

# A container restart executes the same hook again from its writable layer.
rm "$tmpdir/rendered"
RENDERED_HOSTS=$tmpdir/rendered \
GONKA_UPGRADE_BARRIER_STATE=$tmpdir/state \
    "$hook"
[ "$(cat "$tmpdir/rendered")" = versiond ] || fail \
    "the persisted host list was not rendered after restart"

sed '1s/VERSIOND_HOSTS/UNKNOWN_HOSTS/' \
    "$tmpdir/state" >"$tmpdir/invalid-state"
if RENDERED_HOSTS=$tmpdir/rendered \
    GONKA_UPGRADE_BARRIER_STATE=$tmpdir/invalid-state \
    "$hook" >/dev/null 2>&1; then
    fail "invalid persisted state was accepted"
fi

echo "legacy-router-upgrade-barrier_test: ok"
