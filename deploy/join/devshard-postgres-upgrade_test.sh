#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
tmpdir=$(mktemp -d)
project="gonka-pg-upgrade-$$"
guard_project="$project-guard"
base="$script_dir/testdata/postgres-v4.compose.yml"
overlay="$script_dir/testdata/postgres-v5.compose.yml"
recovery_overlay="$script_dir/docker-compose.versiond-postgres-recovery.yml"
preflight="$script_dir/devshard-postgres-migration-preflight.sh"
service=devshard-postgres
export GONKA_POSTGRES_TEST_DATA="$tmpdir/persistent"
export GONKA_POSTGRES_TEST_ENTRYPOINT="$script_dir/devshard-postgres-entrypoint.sh"
export GONKA_POSTGRES_TEST_EXISTING="$tmpdir/existing-versiond"

old_compose=(docker compose --project-name "$project" -f "$base")
new_compose=(docker compose --project-name "$project" -f "$base" -f "$overlay")
guard_old_compose=(docker compose --project-name "$guard_project" -f "$base")
guard_new_compose=(docker compose --project-name "$guard_project" -f "$base" -f "$overlay")
guard_recovery_compose=(docker compose --project-name "$guard_project" -f "$base" -f "$overlay" -f "$recovery_overlay")
guard_old_volume=""
guard_replacement_volume=""

cleanup() {
    "${new_compose[@]}" down --volumes --remove-orphans --timeout 1 \
        >/dev/null 2>&1 || true
    "${guard_new_compose[@]}" down --volumes --remove-orphans --timeout 1 \
        >/dev/null 2>&1 || true
    if [[ -n $guard_old_volume ]]; then
        docker volume rm "$guard_old_volume" >/dev/null 2>&1 || true
    fi
    if [[ -n $guard_replacement_volume ]]; then
        docker volume rm "$guard_replacement_volume" >/dev/null 2>&1 || true
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
    "${new_compose[@]}" logs "$service" >&2 || true
    exit 1
}

wait_for_postgres() {
    local _
    local -a compose=("$@")
    for _ in {1..60}; do
        if "${compose[@]}" exec -T "$service" \
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

"${old_compose[@]}" up -d "$service"
wait_for_postgres "${old_compose[@]}"
"${old_compose[@]}" exec -T "$service" psql \
    -U devshardd -d devshardd -v ON_ERROR_STOP=1 \
    -c "CREATE TABLE migration_probe (value text NOT NULL);" \
    -c "INSERT INTO migration_probe VALUES ('preserved-v4-row');" \
    >/dev/null

old_container=$("${old_compose[@]}" ps -q "$service")
old_volume=$(docker inspect "$old_container" --format \
    '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql/data"}}{{.Name}}{{end}}{{end}}')
[[ -n $old_volume ]] || fail "v4 PostgreSQL did not use an anonymous volume"

# The operator preflight must inspect the live v4 container without stopping it
# and approve the target filesystem before Compose performs the first recreate.
"$preflight" --source-container "$old_container" \
    --target-dir "$GONKA_POSTGRES_TEST_DATA" >/dev/null

# This is the supported upgrade: recreate in place. Compose carries the old
# anonymous mount to the new container, whose entrypoint migrates it.
"${new_compose[@]}" up -d --force-recreate "$service"
wait_for_postgres "${new_compose[@]}"
new_container=$("${new_compose[@]}" ps -q "$service")
preserved_volume=$(docker inspect "$new_container" --format \
    '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql/data"}}{{.Name}}{{end}}{{end}}')
[[ $preserved_volume == "$old_volume" ]] || fail \
    "Compose did not carry the v4 anonymous volume into the upgrade"

row=$("${new_compose[@]}" exec -T "$service" psql \
    -U devshardd -d devshardd -Atc \
    "SELECT value FROM migration_probe;")
[[ $row == preserved-v4-row ]] || fail "the v4 database row was not migrated"
"${new_compose[@]}" exec -T "$service" \
    test -s /var/lib/postgresql/gonka/data/PG_VERSION || fail \
    "persistent PGDATA was not published"
preflight_result=$("$preflight" --source-container "$new_container" \
    --target-dir "$GONKA_POSTGRES_TEST_DATA")
grep -q 'no migration copy is required' <<<"$preflight_result" || fail \
    "preflight did not recognize migrated persistent PGDATA"

# Once migrated, even removing the container is safe: the bind-mounted PGDATA
# is authoritative and the replacement no longer needs the anonymous source.
"${new_compose[@]}" down
"${new_compose[@]}" up -d "$service"
wait_for_postgres "${new_compose[@]}"
row=$("${new_compose[@]}" exec -T "$service" psql \
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

"${guard_old_compose[@]}" up -d "$service"
wait_for_postgres "${guard_old_compose[@]}"
"${guard_old_compose[@]}" exec -T "$service" psql \
    -U devshardd -d devshardd -v ON_ERROR_STOP=1 \
    -c "CREATE TABLE detached_volume_probe (value text NOT NULL);" \
    -c "INSERT INTO detached_volume_probe VALUES ('recovered-v4-row');" \
    >/dev/null
guard_old_container=$("${guard_old_compose[@]}" ps -q "$service")
guard_old_volume=$(docker inspect "$guard_old_container" --format \
    '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql/data"}}{{.Name}}{{end}}{{end}}')
[[ -n $guard_old_volume ]] || fail \
    "guard fixture did not create an anonymous v4 volume"
"${guard_old_compose[@]}" down

"$preflight" --source-volume "$guard_old_volume" \
    --target-dir "$GONKA_POSTGRES_TEST_DATA" >/dev/null

# A completed staging copy is sufficient for a restart. The detached source
# volume and host target must both be visible to the helper in this mode.
restart_target="$tmpdir/recovery-restart"
mkdir -p "$restart_target/.migrating"
printf '16\n' >"$restart_target/.migrating/PG_VERSION"
: >"$restart_target/.migrating/.gonka-copy-complete"
restart_result=$("$preflight" --source-volume "$guard_old_volume" \
    --target-dir "$restart_target")
grep -q 'staging is complete' <<<"$restart_result" || fail \
    "volume recovery preflight did not recognize completed staging"

"${guard_new_compose[@]}" up -d "$service"
guard_container=$("${guard_new_compose[@]}" ps -aq "$service")
for _ in {1..30}; do
    guard_state=$(docker inspect "$guard_container" --format '{{.State.Status}}')
    [[ $guard_state == exited ]] && break
    sleep 0.2
done
[[ ${guard_state:-} == exited ]] || fail \
    "detached-volume guard did not stop PostgreSQL"
guard_logs=$("${guard_new_compose[@]}" logs --no-color "$service" 2>&1)
grep -q 'refusing to initialize an empty database' <<<"$guard_logs" || fail \
    "detached-volume guard did not explain the failure"
[[ ! -e $GONKA_POSTGRES_TEST_DATA/data/PG_VERSION ]] || fail \
    "detached-volume guard initialized an empty PostgreSQL cluster"

# The shipped recovery overlay reattaches the selected external volume at the
# legacy mount. The same entrypoint then performs its validated atomic copy into
# the persistent PGDATA, so operators never have to guess the target level.
export DEVSHARD_POSTGRES_LEGACY_VOLUME=$guard_old_volume
"${guard_recovery_compose[@]}" up -d --force-recreate "$service"
wait_for_postgres "${guard_recovery_compose[@]}"
recovery_container=$("${guard_recovery_compose[@]}" ps -q "$service")
recovery_volume=$(docker inspect "$recovery_container" --format \
    '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql/data"}}{{.Name}}{{end}}{{end}}')
[[ $recovery_volume == "$guard_old_volume" ]] || fail \
    "recovery container did not mount the selected v4 volume"
recovered_row=$("${guard_recovery_compose[@]}" exec -T "$service" psql \
    -U devshardd -d devshardd -Atc \
    "SELECT value FROM detached_volume_probe;")
[[ $recovered_row == recovered-v4-row ]] || fail \
    "detached v4 PostgreSQL volume was not recovered"
"${guard_recovery_compose[@]}" exec -T "$service" \
    test -s /var/lib/postgresql/gonka/data/PG_VERSION || fail \
    "recovery overlay did not publish persistent PGDATA"
restart_result=$("$preflight" --source-volume "$guard_old_volume" \
    --target-dir "$GONKA_POSTGRES_TEST_DATA")
grep -q 'no migration copy is required' <<<"$restart_result" || fail \
    "volume recovery preflight did not recognize published PGDATA"

# Remove the temporary legacy mount and prove the recovered bind is now the
# only storage needed by a normal deployment restart.
"${guard_new_compose[@]}" up -d --force-recreate --renew-anon-volumes "$service"
wait_for_postgres "${guard_new_compose[@]}"
replacement_container=$("${guard_new_compose[@]}" ps -q "$service")
guard_replacement_volume=$(docker inspect "$replacement_container" --format \
    '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql/data"}}{{.Name}}{{end}}{{end}}')
[[ -n $guard_replacement_volume ]] || fail \
    "normal deployment did not create a replacement anonymous volume"
[[ $guard_replacement_volume != "$guard_old_volume" ]] || fail \
    "normal deployment retained the temporary recovery volume"
docker volume inspect "$guard_old_volume" >/dev/null || fail \
    "detaching the recovery mount removed its rollback volume"
recovered_row=$("${guard_new_compose[@]}" exec -T "$service" psql \
    -U devshardd -d devshardd -Atc \
    "SELECT value FROM detached_volume_probe;")
[[ $recovered_row == recovered-v4-row ]] || fail \
    "recovered PostgreSQL data was lost after detaching the v4 volume"

echo "devshard-postgres-upgrade_test: ok"
