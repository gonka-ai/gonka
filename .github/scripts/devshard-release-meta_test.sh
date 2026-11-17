#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
meta=$script_dir/devshard-release-meta.sh
chmod +x "$meta"

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

fail() {
	printf 'devshard-release-meta_test: %s\n' "$*" >&2
	exit 1
}

field() {
	local key=$1 blob=$2
	printf '%s\n' "$blob" | sed -n "s/^${key}=//p" | head -n 1
}

assert_eq() {
	local got=$1 want=$2 msg=$3
	[[ $got == "$want" ]] || fail "$msg: got '$got' want '$want'"
}

assert_fail() {
	local msg=$1
	shift
	local rc=0
	"$meta" "$@" >"$tmpdir/out" 2>"$tmpdir/err" || rc=$?
	[[ $rc -ne 0 ]] || fail "$msg: expected failure, stdout=$(cat "$tmpdir/out")"
}

got=""
run_ok() {
	got=$("$meta" "$@") || fail "expected success for: $*"
}

run_ok tag --ref-name release/v0.2.14
assert_eq "$(field mode "$got")" chain "release/v0.2.14 mode"
assert_eq "$(field build_inference_chain "$got")" true "v0.2.14 inference-chain"
assert_eq "$(field build_dapi "$got")" true "v0.2.14 dapi"
assert_eq "$(field build_edge_api "$got")" true "v0.2.14 edge-api"
assert_eq "$(field build_devshardd "$got")" false "v0.2.14 host"
assert_eq "$(field chain_version "$got")" v0.2.14 "v0.2.14 VERSION"
assert_eq "$(field race_releases_tag "$got")" release/v0.2.14 "v0.2.14 race tag"

run_ok tag --ref-name release/v0.2.14-rc1
assert_eq "$(field mode "$got")" chain "rc1 mode"
assert_eq "$(field chain_version "$got")" v0.2.14-rc1 "rc1 VERSION"
assert_eq "$(field build_devshardd "$got")" false "rc1 host"

run_ok tag --ref-name release/v0.2.14-devshard-v4.0.0 --github-sha abc
assert_eq "$(field mode "$got")" host "v4.0.0 mode"
assert_eq "$(field build_inference_chain "$got")" false "v4.0.0 chain"
assert_eq "$(field build_devshardd "$got")" true "v4.0.0 host"
assert_eq "$(field make_devshard_version "$got")" v4 "v4.0.0 make slot"
assert_eq "$(field devshard_protocol_version "$got")" v4 "v4.0.0 protocol"
assert_eq "$(field devshard_version "$got")" v4 "v4.0.0 stripped"
assert_eq "$(field devshard_binary_version "$got")" v4.0.0 "v4.0.0 binary"
assert_eq "$(field release_name "$got")" "Devshard Release v4.0.0" "v4.0.0 name"
assert_eq "$(field release_tag "$got")" "devshard/v4.0.0" "v4.0.0 tag"
assert_eq "$(field release_body_line "$got")" "devshardd v4 protocol v4 binary stamp v4.0.0" "v4.0.0 body"

name_a=$(field release_name "$got")
tag_a=$(field release_tag "$got")
run_ok tag --ref-name release/v0.2.15-devshard-v4.0.0
assert_eq "$(field release_name "$got")" "$name_a" "same leftover same name"
assert_eq "$(field release_tag "$got")" "$tag_a" "same leftover same tag"

run_ok tag --ref-name release/devshard/v4.1.0
assert_eq "$(field mode "$got")" host "v4.1.0 mode"
assert_eq "$(field make_devshard_version "$got")" v4 "v4.1.0 make slot"
assert_eq "$(field devshard_version "$got")" v4.1 "v4.1.0 stripped"
assert_eq "$(field devshard_binary_version "$got")" v4.1.0 "v4.1.0 binary"
assert_eq "$(field release_body_line "$got")" "devshardd v4.1 protocol v4 binary stamp v4.1.0" "v4.1.0 body"
assert_eq "$(field release_name "$got")" "Devshard Release v4.1.0" "v4.1.0 name"
assert_eq "$(field release_tag "$got")" "devshard/v4.1.0" "v4.1.0 tag"

run_ok tag --ref-name release/devshard/v4.1.1
assert_eq "$(field devshard_version "$got")" v4.1.1 "v4.1.1 stripped"
assert_eq "$(field release_body_line "$got")" "devshardd v4.1.1 protocol v4 binary stamp v4.1.1" "v4.1.1 body"

run_ok tag --ref-name release/devshard/v6.0.0-rc1
assert_eq "$(field mode "$got")" host "v6.0.0-rc1 mode"
assert_eq "$(field build_inference_chain "$got")" false "v6.0.0-rc1 chain"
assert_eq "$(field build_devshardd "$got")" true "v6.0.0-rc1 host"
assert_eq "$(field make_devshard_version "$got")" v6 "v6.0.0-rc1 make slot"
assert_eq "$(field devshard_protocol_version "$got")" v6 "v6.0.0-rc1 protocol"
assert_eq "$(field devshard_version "$got")" v6 "v6.0.0-rc1 stripped"
assert_eq "$(field devshard_binary_version "$got")" v6.0.0-rc1 "v6.0.0-rc1 binary"
assert_eq "$(field release_name "$got")" "Devshard Release v6.0.0-rc1" "v6.0.0-rc1 name"
assert_eq "$(field release_tag "$got")" "devshard/v6.0.0-rc1" "v6.0.0-rc1 tag"
assert_eq "$(field release_body_line "$got")" "devshardd v6 protocol v6 binary stamp v6.0.0-rc1 release candidate 1" "v6.0.0-rc1 body"

run_ok tag --ref-name release/devshard/v4.1.0-rc2
assert_eq "$(field devshard_version "$got")" v4.1 "v4.1.0-rc2 stripped"
assert_eq "$(field devshard_binary_version "$got")" v4.1.0-rc2 "v4.1.0-rc2 binary"
assert_eq "$(field release_name "$got")" "Devshard Release v4.1.0-rc2" "v4.1.0-rc2 name"
assert_eq "$(field release_tag "$got")" "devshard/v4.1.0-rc2" "v4.1.0-rc2 tag"
assert_eq "$(field release_body_line "$got")" "devshardd v4.1 protocol v4 binary stamp v4.1.0-rc2 release candidate 2" "v4.1.0-rc2 body"

run_ok tag --ref-name release/v0.2.16-devshard-v6.0.0-rc1
assert_eq "$(field mode "$got")" host "chain-shaped rc mode"
assert_eq "$(field devshard_binary_version "$got")" v6.0.0-rc1 "chain-shaped rc binary"
assert_eq "$(field release_tag "$got")" "devshard/v6.0.0-rc1" "chain-shaped rc tag"
assert_eq "$(field release_body_line "$got")" "devshardd v6 protocol v6 binary stamp v6.0.0-rc1 release candidate 1" "chain-shaped rc body"

run_ok tag --ref-name release/devshard/v4.1.1-rc10
assert_eq "$(field devshard_version "$got")" v4.1.1 "v4.1.1-rc10 keeps patch"
assert_eq "$(field release_body_line "$got")" "devshardd v4.1.1 protocol v4 binary stamp v4.1.1-rc10 release candidate 10" "v4.1.1-rc10 body"

run_ok leftover-from-ref --ref-name release/devshard/v4.1.0
assert_eq "${got//$'\n'/}" v4.1.0 "leftover-from-ref short host tag"
run_ok leftover-from-ref --ref-name release/devshard/v6.0.0-rc1
assert_eq "${got//$'\n'/}" v6.0.0-rc1 "leftover-from-ref short rc tag"
run_ok leftover-from-ref --ref-name release/v0.2.15-devshard-v5.0.0
assert_eq "${got//$'\n'/}" v5.0.0 "leftover-from-ref chain-shaped host tag"
run_ok leftover-from-ref --ref-name release/v0.2.16-devshard-v6.0.0-rc1
assert_eq "${got//$'\n'/}" v6.0.0-rc1 "leftover-from-ref chain-shaped rc tag"
run_ok leftover-from-ref --ref-name release/v0.2.16-rc1-devshard-v6.0.0-rc2
assert_eq "${got//$'\n'/}" v6.0.0-rc2 "leftover-from-ref chain rc and host rc"
assert_fail "chain tag is not leftover" leftover-from-ref --ref-name release/v0.2.15
assert_fail "short leftover-from-ref" leftover-from-ref --ref-name release/devshard/v4.1

run_ok docker-tag --ref-name release/v0.2.15-devshard-v5.0.0 --owner gonka-ai
assert_eq "$(field leftover "$got")" v5.0.0 "docker leftover"
assert_eq "$(field leftover_major "$got")" v5 "docker leftover major"
assert_eq "$(field chain_version "$got")" v0.2.15 "docker chain"
assert_eq "$(field image_tag "$got")" 0.2.15-devshard-v5 "docker image tag strips leftover to major"
assert_eq "$(field release_name "$got")" "Devshard Release v5.0.0" "docker release name matches host zip"
assert_eq "$(field release_tag "$got")" "devshard/v5.0.0" "docker release tag matches host zip"
assert_eq "$(field release_body_line "$got")" "devshardd v5 protocol v5 binary stamp v5.0.0" "docker body line"
assert_eq "$(field image_versiond "$got")" "ghcr.io/gonka-ai/versiond:0.2.15-devshard-v5" "docker versiond image"
assert_eq "$(field image_versiond_router "$got")" "ghcr.io/gonka-ai/versiond-router:0.2.15-devshard-v5" "docker router image"
assert_eq "$(field image_proxy "$got")" "ghcr.io/gonka-ai/proxy:0.2.15-devshard-v5" "docker proxy image"
assert_eq "$(field image_proxy_router "$got")" "ghcr.io/gonka-ai/proxy-router:0.2.15-devshard-v5" "docker proxy-router image"

run_ok docker-tag --ref-name release/v0.2.15-devshard-v5.1.0 --owner gonka-ai
assert_eq "$(field image_tag "$got")" 0.2.15-devshard-v5 "v5.1.0 still majors to v5"
assert_eq "$(field release_name "$got")" "Devshard Release v5.1.0" "release name keeps full leftover"

run_ok docker-tag --ref-name release/v0.2.15-rc1-devshard-v5.0.0 --owner gonka-ai --registry ghcr.io
assert_eq "$(field image_tag "$got")" 0.2.15-rc1-devshard-v5 "rc stays in chain slice"

run_ok docker-tag --ref-name release/v0.2.16-devshard-v6.0.0-rc1 --owner gonka-ai
assert_eq "$(field leftover "$got")" v6.0.0-rc1 "docker rc leftover"
assert_eq "$(field leftover_major "$got")" v6 "docker rc major ignores candidate"
assert_eq "$(field chain_version "$got")" v0.2.16 "docker rc chain"
assert_eq "$(field image_tag "$got")" 0.2.16-devshard-v6-rc1 "docker image tag keeps candidate"
assert_eq "$(field release_name "$got")" "Devshard Release v6.0.0-rc1" "docker rc release name"
assert_eq "$(field release_tag "$got")" "devshard/v6.0.0-rc1" "docker rc release tag"
assert_eq "$(field release_body_line "$got")" "devshardd v6 protocol v6 binary stamp v6.0.0-rc1 release candidate 1" "docker rc body"
assert_eq "$(field image_versiond "$got")" "ghcr.io/gonka-ai/versiond:0.2.16-devshard-v6-rc1" "docker rc versiond image"

run_ok docker-tag --ref-name release/v0.2.16-rc1-devshard-v6.0.0-rc2 --owner gonka-ai
assert_eq "$(field chain_version "$got")" v0.2.16-rc1 "docker keeps chain candidate"
assert_eq "$(field image_tag "$got")" 0.2.16-rc1-devshard-v6-rc2 "docker keeps both candidates"
assert_eq "$(field release_body_line "$got")" "devshardd v6 protocol v6 binary stamp v6.0.0-rc2 release candidate 2" "docker host candidate body"

assert_fail "short host tag has no chain version" docker-tag --ref-name release/devshard/v5.0.0 --owner gonka-ai
assert_fail "chain tag is not docker-tag" docker-tag --ref-name release/v0.2.15 --owner gonka-ai

assert_fail "v4.1 leftover" tag --ref-name release/devshard/v4.1
assert_fail "bare rc" tag --ref-name release/devshard/v4.1.0-rc
assert_fail "rc zero" tag --ref-name release/devshard/v4.1.0-rc0
assert_fail "rc leading zero" tag --ref-name release/devshard/v4.1.0-rc01
assert_fail "rc uppercase" tag --ref-name release/devshard/v4.1.0-RC1
assert_fail "short leftover on chain-shaped tag" tag --ref-name release/v0.2.14-devshard-v4
assert_fail "junk after leftover" tag --ref-name release/v0.2.14-devshard-v4.0.0-extra
assert_fail "junk after rc" tag --ref-name release/v0.2.14-devshard-v4.0.0-rc1-extra

assert_fail "no boxes" dispatch \
	--build-inference-chain false --build-dapi false --build-edge-api false --build-devshardd false

assert_fail "host missing binary" dispatch \
	--build-inference-chain false --build-dapi false --build-edge-api false --build-devshardd true \
	--devshard-version v4.1 --devshard-protocol-version v4

assert_fail "host missing version" dispatch \
	--build-inference-chain false --build-dapi false --build-edge-api false --build-devshardd true \
	--devshard-protocol-version v4 --devshard-binary-version v4.1.0

assert_fail "host missing protocol" dispatch \
	--build-inference-chain false --build-dapi false --build-edge-api false --build-devshardd true \
	--devshard-version v4.1 --devshard-binary-version v4.1.0

assert_fail "binary not three-part" dispatch \
	--build-inference-chain false --build-dapi false --build-edge-api false --build-devshardd true \
	--devshard-version v4.1 --devshard-protocol-version v4 --devshard-binary-version v4.1

assert_fail "chain missing version" dispatch \
	--build-inference-chain true --build-dapi false --build-edge-api false --build-devshardd false

run_ok dispatch \
	--build-inference-chain false --build-dapi false --build-edge-api false --build-devshardd true \
	--devshard-version v4.1 --devshard-protocol-version v4 --devshard-binary-version v4.1.0
assert_eq "$(field mode "$got")" host "dispatch host mode"
assert_eq "$(field make_devshard_version "$got")" v4 "dispatch make slot uses protocol"
assert_eq "$(field release_name "$got")" "Devshard Release v4.1.0" "dispatch same name as tag leftover"
assert_eq "$(field release_tag "$got")" "devshard/v4.1.0" "dispatch same tag as leftover"
assert_eq "$(field release_body_line "$got")" "devshardd v4.1 protocol v4 binary stamp v4.1.0" "dispatch body"

run_ok dispatch \
	--build-inference-chain false --build-dapi false --build-edge-api false --build-devshardd true \
	--devshard-version v6 --devshard-protocol-version v6 --devshard-binary-version v6.0.0-rc1
assert_eq "$(field devshard_binary_version "$got")" v6.0.0-rc1 "dispatch rc binary"
assert_eq "$(field make_devshard_version "$got")" v6 "dispatch rc make slot uses protocol input"
assert_eq "$(field release_name "$got")" "Devshard Release v6.0.0-rc1" "dispatch rc name"
assert_eq "$(field release_tag "$got")" "devshard/v6.0.0-rc1" "dispatch rc tag"
assert_eq "$(field release_body_line "$got")" "devshardd v6 protocol v6 binary stamp v6.0.0-rc1 release candidate 1" "dispatch rc body"

run_ok dispatch \
	--build-inference-chain true --build-dapi true --build-edge-api false --build-devshardd false \
	--chain-version v0.2.14
assert_eq "$(field mode "$got")" chain "dispatch chain mode"
assert_eq "$(field build_edge_api "$got")" false "dispatch subset edge-api"
assert_eq "$(field race_releases_tag "$got")" release/v0.2.14 "dispatch default race tag"
assert_eq "$(field build_devshardd "$got")" false "dispatch chain-only host"

run_ok dispatch \
	--build-inference-chain true --build-dapi false --build-edge-api false --build-devshardd true \
	--chain-version v0.2.14 \
	--devshard-version v4.1 --devshard-protocol-version v4 --devshard-binary-version v4.1.0
assert_eq "$(field mode "$got")" both "dispatch both"

cat >"$tmpdir/fake-devshardd" <<'EOF'
#!/usr/bin/env bash
case "${1:-}" in
--print-protocol-version) printf 'v4\n' ;;
--print-binary-version) printf 'v4.1.0\n' ;;
*) exit 2 ;;
esac
EOF
chmod +x "$tmpdir/fake-devshardd"
"$meta" check-stamps --binary "$tmpdir/fake-devshardd" --protocol v4 --binary-version v4.1.0 \
	|| fail "matching stamps should pass"
if "$meta" check-stamps --binary "$tmpdir/fake-devshardd" --protocol v5 --binary-version v4.1.0 \
	>"$tmpdir/out" 2>"$tmpdir/err"; then
	fail "mismatched protocol should fail"
fi
if "$meta" check-stamps --binary "$tmpdir/fake-devshardd" --protocol v4 --binary-version v4.0.0 \
	>"$tmpdir/out" 2>"$tmpdir/err"; then
	fail "mismatched binary stamp should fail"
fi

mkdir -p "$tmpdir/bin"
cat >"$tmpdir/bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${FAKE_DOCKER_LOG:?}"
flag=""
mounted=""
image=""
prev=""
for arg in "$@"; do
	case "$prev" in
	-v)
		mounted=$arg
		;;
	esac
	case "$arg" in
	--print-protocol-version | --print-binary-version)
		flag=$arg
		;;
	devshardd-builder:latest)
		image=$arg
		;;
	esac
	prev=$arg
done
[[ $image == devshardd-builder:latest ]] || exit 3
[[ $mounted == *:/devshardd:ro ]] || exit 4
case "$flag" in
--print-protocol-version) printf 'v4\n' ;;
--print-binary-version) printf 'v4.1.0\n' ;;
*) exit 2 ;;
esac
EOF
chmod +x "$tmpdir/bin/docker"
export FAKE_DOCKER_LOG=$tmpdir/docker.argv
: >"$FAKE_DOCKER_LOG"
PATH="$tmpdir/bin:$PATH" "$meta" check-stamps \
	--binary "$tmpdir/fake-devshardd" --protocol v4 --binary-version v4.1.0 \
	--docker-image devshardd-builder:latest || fail "docker check-stamps should pass"
grep -q -- '--entrypoint /devshardd' "$FAKE_DOCKER_LOG" || fail "docker should override entrypoint"
grep -q -- '--print-protocol-version' "$FAKE_DOCKER_LOG" || fail "docker should print protocol"
grep -q -- '--print-binary-version' "$FAKE_DOCKER_LOG" || fail "docker should print binary"

printf 'devshard-release-meta_test: ok\n'
