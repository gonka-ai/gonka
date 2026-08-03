#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
tmpdir=$(mktemp -d)
project="gonka-pg-upgrade-$$"
guard_project="$project-guard"
base="$script_dir/testdata/postgres-v4.compose.yml"
overlay="$script_dir/testdata/postgres-v5.compose.yml"
export GONKA_POSTGRES_TEST_DATA="$tmpdir/persistent"
export GONKA_POSTGRES_TEST_ENTRYPOINT="$script_dir/devshard-postgres-entrypoint.sh"
export GONKA_POSTGRES_TEST_EXISTING="$tmpdir/existing-versiond"

old_compose=(docker compose --project-name "$project" -f "$base")
new_compose=(docker compose --project-name "$project" -f "$base" -f "$overlay")
guard_old_compose=(docker compose --project-name "$guard_project" -f "$base")
guard_new_compose=(docker compose --project-name "$guard_project" -f "$base" -f "$overlay")
guard_old_volume=""

cleanup() {
    "${new_compose[@]}" down --volumes --remove-orphans --timeout 1 \
        >/dev/null 2>&1 || true
    "${guard_new_compose[@]}" down --volumes --remove-orphans --timeout 1 \
        >/dev/null 2>&1 || true
    if [[ -n $guard_old_volume ]]; then
        docker volume rm "$guard_old_volume" >/dev/null 2>&1 || true
    fi
    docker run --rm --entrypoint sh -v "$tmpdir:/cleanup" \
        postgres:16-alpine -c \
        'rm -rf /cleanup/* /cleanup/.[!.]* /cleanup/..?*' \
        >/dev/null 2>&1 || true
    rm -rf "$tmpdir"
}
trap cleanup EXIT

fail() {
    echo "devshard-postgres-upgrade_test: $*" >&2
    "${new_compose[@]}" logs postgres >&2 || true
    exit 1
}

wait_for_postgres() {
    local _
    local -a compose=("$@")
    for _ in {1..60}; do
        if "${compose[@]}" exec -T postgres \
            pg_isready -U devshardd -d devshardd >/dev/null 2>&1; then
            return 0
        fi
        sleep 1
    done
    fail "PostgreSQL did not become ready"
}

mkdir -p "$GONKA_POSTGRES_TEST_DATA" \
    "$GONKA_POSTGRES_TEST_EXISTING/v4"
printf 'existing v4 installation\n' \
    >"$GONKA_POSTGRES_TEST_EXISTING/v4/install.json"

"${old_compose[@]}" up -d postgres
wait_for_postgres "${old_compose[@]}"
"${old_compose[@]}" exec -T postgres psql \
    -U devshardd -d devshardd -v ON_ERROR_STOP=1 \
    -c "CREATE TABLE migration_probe (value text NOT NULL);" \
    -c "INSERT INTO migration_probe VALUES ('preserved-v4-row');" \
    >/dev/null

old_container=$("${old_compose[@]}" ps -q postgres)
old_volume=$(docker inspect "$old_container" --format \
    '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql/data"}}{{.Name}}{{end}}{{end}}')
[[ -n $old_volume ]] || fail "v4 PostgreSQL did not use an anonymous volume"

# This is the supported upgrade: recreate in place. Compose carries the old
# anonymous mount to the new container, whose entrypoint migrates it.
"${new_compose[@]}" up -d --force-recreate postgres
wait_for_postgres "${new_compose[@]}"
new_container=$("${new_compose[@]}" ps -q postgres)
preserved_volume=$(docker inspect "$new_container" --format \
    '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql/data"}}{{.Name}}{{end}}{{end}}')
[[ $preserved_volume == "$old_volume" ]] || fail \
    "Compose did not carry the v4 anonymous volume into the upgrade"

row=$("${new_compose[@]}" exec -T postgres psql \
    -U devshardd -d devshardd -Atc \
    "SELECT value FROM migration_probe;")
[[ $row == preserved-v4-row ]] || fail "the v4 database row was not migrated"
"${new_compose[@]}" exec -T postgres \
    test -s /var/lib/postgresql/gonka/data/PG_VERSION || fail \
    "persistent PGDATA was not published"

# Once migrated, even removing the container is safe: the bind-mounted PGDATA
# is authoritative and the replacement no longer needs the anonymous source.
"${new_compose[@]}" down
"${new_compose[@]}" up -d postgres
wait_for_postgres "${new_compose[@]}"
row=$("${new_compose[@]}" exec -T postgres psql \
    -U devshardd -d devshardd -Atc \
    "SELECT value FROM migration_probe;")
[[ $row == preserved-v4-row ]] || fail \
    "persistent database row was lost across down/up"

# If an operator removed the v4 container before migration, Docker cannot know
# which dangling anonymous volume belonged to it. The new service must fail
# closed rather than initialize an empty database beside an existing install.
export GONKA_POSTGRES_TEST_DATA="$tmpdir/guard-persistent"
export GONKA_POSTGRES_TEST_EXISTING="$tmpdir/guard-existing-versiond"
mkdir -p "$GONKA_POSTGRES_TEST_DATA" \
    "$GONKA_POSTGRES_TEST_EXISTING/v4"
printf 'existing v4 installation\n' \
    >"$GONKA_POSTGRES_TEST_EXISTING/v4/install.json"

"${guard_old_compose[@]}" up -d postgres
wait_for_postgres "${guard_old_compose[@]}"
guard_old_container=$("${guard_old_compose[@]}" ps -q postgres)
guard_old_volume=$(docker inspect "$guard_old_container" --format \
    '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql/data"}}{{.Name}}{{end}}{{end}}')
[[ -n $guard_old_volume ]] || fail \
    "guard fixture did not create an anonymous v4 volume"
"${guard_old_compose[@]}" down

"${guard_new_compose[@]}" up -d postgres
guard_container=$("${guard_new_compose[@]}" ps -aq postgres)
for _ in {1..30}; do
    guard_state=$(docker inspect "$guard_container" --format '{{.State.Status}}')
    [[ $guard_state == exited ]] && break
    sleep 0.2
done
[[ ${guard_state:-} == exited ]] || fail \
    "detached-volume guard did not stop PostgreSQL"
guard_logs=$("${guard_new_compose[@]}" logs --no-color postgres 2>&1)
grep -q 'refusing to initialize an empty database' <<<"$guard_logs" || fail \
    "detached-volume guard did not explain the failure"
[[ ! -e $GONKA_POSTGRES_TEST_DATA/data/PG_VERSION ]] || fail \
    "detached-volume guard initialized an empty PostgreSQL cluster"

echo "devshard-postgres-upgrade_test: ok"
