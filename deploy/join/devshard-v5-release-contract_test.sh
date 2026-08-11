#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

fail() {
    echo "devshard-v5-release-contract_test: $*" >&2
    exit 1
}

# shellcheck source=deploy/join/devshard-v5-release-contract.sh
source "$script_dir/devshard-v5-release-contract.sh"
devshard_v5_load_release_contract "$script_dir/devshard-v5-release.env"

sed 's|^DEVSHARD_V5_POSTGRES_IMAGE=.*|DEVSHARD_V5_POSTGRES_IMAGE=postgres:16-alpine|' \
    "$script_dir/devshard-v5-release.env" >"$tmpdir/mutable-postgres.env"
if (devshard_v5_load_release_contract "$tmpdir/mutable-postgres.env") \
    >"$tmpdir/mutable-postgres.stdout" 2>"$tmpdir/mutable-postgres.stderr"; then
    fail "release contract accepted a mutable PostgreSQL tag"
fi
grep -q 'POSTGRES_IMAGE must use an immutable sha256 digest' \
    "$tmpdir/mutable-postgres.stderr" || {
        cat "$tmpdir/mutable-postgres.stderr" >&2
        fail "mutable PostgreSQL failure was not actionable"
    }

devshard_v5_version_at_least 2.20.0 2.20.0 || fail "equal version failed"
devshard_v5_version_at_least v2.31.1 2.20.0 || fail "newer version failed"
if devshard_v5_version_at_least 2.19.9 2.20.0; then
    fail "older version passed"
fi

repo=$tmpdir/repo
mkdir -p "$repo/deploy/join"
git -C "$repo" init -q
git -C "$repo" config user.name contract-test
git -C "$repo" config user.email contract-test@example.invalid
while IFS= read -r path; do
    mkdir -p "$repo/$(dirname -- "$path")"
    printf 'release file: %s\n' "$path" >"$repo/$path"
done < <(devshard_v5_release_critical_paths)
printf 'services: {}\n' >"$repo/deploy/join/docker-compose.yml"
git -C "$repo" add .
git -C "$repo" commit -qm 'release contract fixture'
git -c tag.gpgSign=false -c tag.forceSignAnnotated=false \
    -C "$repo" tag --no-sign "$DEVSHARD_V5_RELEASE_GIT_TAG"

if DEVSHARD_V5_ALLOW_UNRELEASED_SOURCE=false \
    devshard_v5_verify_release_source "$repo/deploy/join" \
    >"$tmpdir/mutable.stdout" 2>"$tmpdir/mutable.stderr"; then
    fail "production release accepted mutable image tags"
fi
grep -q 'must be pinned as repo@sha256:digest' "$tmpdir/mutable.stderr" || {
    cat "$tmpdir/mutable.stderr" >&2
    fail "mutable image failure was not actionable"
}

digest=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
DEVSHARD_V5_EDGE_API_IMAGE=${DEVSHARD_V5_EDGE_API_IMAGE%:*}@sha256:$digest
DEVSHARD_V5_VERSIOND_IMAGE=${DEVSHARD_V5_VERSIOND_IMAGE%:*}@sha256:$digest
DEVSHARD_V5_EDGE_API_ROUTER_IMAGE=${DEVSHARD_V5_EDGE_API_ROUTER_IMAGE%:*}@sha256:$digest
DEVSHARD_V5_VERSIOND_ROUTER_IMAGE=${DEVSHARD_V5_VERSIOND_ROUTER_IMAGE%:*}@sha256:$digest
DEVSHARD_V5_PROXY_POLICY_IMAGE=${DEVSHARD_V5_PROXY_POLICY_IMAGE%:*}@sha256:$digest
DEVSHARD_V5_PROXY_ROUTER_IMAGE=${DEVSHARD_V5_PROXY_ROUTER_IMAGE%:*}@sha256:$digest

DEVSHARD_V5_ALLOW_UNRELEASED_SOURCE=false \
    devshard_v5_verify_release_source "$repo/deploy/join" \
    >"$tmpdir/source.stdout" 2>"$tmpdir/source.stderr" || {
        cat "$tmpdir/source.stderr" >&2
        fail "tagged release source was rejected"
    }
[[ $DEVSHARD_V5_RELEASE_COMMIT == \
    "$(git -C "$repo" rev-parse HEAD)" ]] || fail \
    "release source did not expose the tagged commit"

printf '# local operator override\n' >>"$repo/deploy/join/docker-compose.yml"
devshard_v5_report_compose_files \
    "$repo/deploy/join" "$repo/deploy/join/docker-compose.yml" \
    >"$tmpdir/compose.stdout"
grep -q 'source=local-override' "$tmpdir/compose.stdout" || {
    cat "$tmpdir/compose.stdout" >&2
    fail "local Compose customization was not preserved as an override"
}

printf '# accidental edit\n' >>"$repo/deploy/join/upgrade-devshard-v5.sh"
if DEVSHARD_V5_ALLOW_UNRELEASED_SOURCE=false \
    devshard_v5_verify_release_source "$repo/deploy/join" \
    >"$tmpdir/modified.stdout" 2>"$tmpdir/modified.stderr"; then
    fail "modified release-critical file was accepted"
fi
grep -q 'differs from immutable release' "$tmpdir/modified.stderr" || {
    cat "$tmpdir/modified.stderr" >&2
    fail "modified release source failure was not actionable"
}

echo "devshard-v5-release-contract_test: ok"
