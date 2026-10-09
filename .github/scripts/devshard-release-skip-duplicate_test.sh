#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
skip=$script_dir/devshard-release-skip-duplicate.sh
meta=$script_dir/devshard-release-meta.sh
chmod +x "$skip" "$meta"

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

fail() {
	printf 'devshard-release-skip-duplicate_test: %s\n' "$*" >&2
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

state=$tmpdir/gh
mkdir -p "$state/bin"
: >"$state/argv.log"
for status in queued in_progress requested waiting pending; do
	printf '[]\n' >"$state/$status.json"
done

cat >"$state/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$FAKE_GH_STATE/argv.log"
status=""
while [[ $# -gt 0 ]]; do
	case "$1" in
	--status)
		status=$2
		shift 2
		;;
	*)
		shift
		;;
	esac
done
[[ -n $status ]] || { echo '[]'; exit 0; }
if [[ -f $FAKE_GH_STATE/$status.json ]]; then
	cat "$FAKE_GH_STATE/$status.json"
else
	printf '[]\n'
fi
EOF
chmod +x "$state/bin/gh"

export FAKE_GH_STATE=$state
export GH_PATH=$state/bin/gh
export META_PATH=$meta

got=""
run_skip() {
	got=$("$skip" "$@") || fail "skip helper failed for: $*"
}

reset_lists() {
	for status in queued in_progress requested waiting pending; do
		printf '[]\n' >"$state/$status.json"
	done
}

run_skip --run-id 200 --sha abc --release-tag devshard/v5.0.0
assert_eq "$(field skip "$got")" false "empty lists"

printf '%s\n' '[{"databaseId":100,"headSha":"other","headBranch":"release/devshard/v5.0.0"}]' \
	>"$state/queued.json"
run_skip --run-id 200 --sha abc --release-tag devshard/v5.0.0
assert_eq "$(field skip "$got")" true "older queued same leftover"
[[ $(field reason "$got") == *100* ]] || fail "reason should mention older run id"

reset_lists
printf '%s\n' '[{"databaseId":100,"headSha":"abc","headBranch":"release/v0.2.15-devshard-v5.0.0"}]' \
	>"$state/in_progress.json"
run_skip --run-id 200 --sha abc --release-tag devshard/v5.0.0
assert_eq "$(field skip "$got")" true "older in_progress same sha host tag"

reset_lists
printf '%s\n' '[{"databaseId":100,"headSha":"abc","headBranch":"release/v0.2.15"}]' \
	>"$state/in_progress.json"
run_skip --run-id 200 --sha abc --release-tag devshard/v5.0.0
assert_eq "$(field skip "$got")" false "same sha chain tag is not a host duplicate"

reset_lists
printf '%s\n' '[{"databaseId":300,"headSha":"other","headBranch":"release/devshard/v5.0.0"}]' \
	>"$state/queued.json"
run_skip --run-id 200 --sha abc --release-tag devshard/v5.0.0
assert_eq "$(field skip "$got")" false "newer run does not skip first-wins"

reset_lists
printf '%s\n' '[{"databaseId":200,"headSha":"abc","headBranch":"release/devshard/v5.0.0"}]' \
	>"$state/in_progress.json"
run_skip --run-id 200 --sha abc --release-tag devshard/v5.0.0
assert_eq "$(field skip "$got")" false "do not skip ourselves"

reset_lists
printf '%s\n' '[{"databaseId":50,"headSha":"abc","headBranch":"release/devshard/v4.1.0"}]' \
	>"$state/waiting.json"
run_skip --run-id 200 --sha abc --release-tag devshard/v5.0.0
assert_eq "$(field skip "$got")" true "same sha different leftover still skips"

reset_lists
printf '%s\n' '[{"databaseId":50,"headSha":"zzz","headBranch":"release/v0.2.14-devshard-v5.0.0"}]' \
	>"$state/pending.json"
run_skip --run-id 200 --sha abc --release-tag devshard/v5.0.0
assert_eq "$(field skip "$got")" true "same leftover maps from chain-shaped host tag"

grep -q -- '--workflow publish_upgrade_binaries.yml' "$state/argv.log" ||
	fail "gh run list should target publish_upgrade_binaries.yml"

printf 'devshard-release-skip-duplicate_test: ok\n'
