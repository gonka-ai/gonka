#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
exists=$script_dir/devshard-ghcr-tag-exists.sh
chmod +x "$exists"

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

fail() {
	printf 'devshard-ghcr-tag-exists_test: %s\n' "$*" >&2
	exit 1
}

state=$tmpdir/gh
mkdir -p "$state/bin"
printf '[]\n' >"$state/versions.json"
cat >"$state/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$FAKE_GH_STATE/argv.log"
if [[ ${1:-} == api ]]; then
	if [[ -f $FAKE_GH_STATE/fail ]]; then
		exit 1
	fi
	cat "$FAKE_GH_STATE/versions.json"
	exit 0
fi
exit 2
EOF
chmod +x "$state/bin/gh"
export FAKE_GH_STATE=$state
export GH_PATH=$state/bin/gh

if "$exists" --owner gonka-ai --package versiond --tag 0.2.15-devshard-v5; then
	fail "empty versions should be missing"
fi

python3 - "$state/versions.json" <<'PY'
import json, pathlib, os
pathlib.Path(os.environ["FAKE_GH_STATE"], "versions.json").write_text(json.dumps([
    {"metadata": {"container": {"tags": ["latest", "0.2.15-devshard-v5"]}}}
]))
PY

"$exists" --owner gonka-ai --package versiond --tag 0.2.15-devshard-v5 \
	|| fail "tag in versions should exist"

if "$exists" --owner gonka-ai --package versiond --tag other; then
	fail "other tag should be missing"
fi

touch "$state/fail"
if "$exists" --owner gonka-ai --package versiond --tag 0.2.15-devshard-v5; then
	fail "package 404 should be missing"
fi

grep -q 'orgs/gonka-ai/packages/container/versiond/versions' "$state/argv.log" \
	|| fail "should query org package versions"

printf 'devshard-ghcr-tag-exists_test: ok\n'
