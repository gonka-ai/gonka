#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
upsert=$script_dir/devshard-release-upsert-notes.sh
chmod +x "$upsert"

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

fail() {
	printf 'devshard-release-upsert-notes_test: %s\n' "$*" >&2
	exit 1
}

state=$tmpdir/gh
mkdir -p "$state/bin"
printf '[]\n' >"$state/releases.json"
: >"$state/argv.log"

cat >"$state/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$FAKE_GH_STATE/argv.log"
cmd=${1:-}
shift || true
case "$cmd" in
api)
	cat "$FAKE_GH_STATE/releases.json"
	;;
release)
	sub=${1:-}
	shift || true
	case "$sub" in
	create)
		tag=""
		title=""
		notes=""
		target=""
		while [[ $# -gt 0 ]]; do
			case "$1" in
			--repo) shift 2 ;;
			--title)
				title=$2
				shift 2
				;;
			--notes)
				notes=$2
				shift 2
				;;
			--target)
				target=$2
				shift 2
				;;
			--prerelease)
				shift
				;;
			*)
				if [[ -z $tag ]]; then
					tag=$1
				fi
				shift
				;;
			esac
		done
		python3 - "$FAKE_GH_STATE/releases.json" "$title" "$tag" "$notes" "$target" <<'PY'
import json, sys
path, title, tag, notes, target = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4], sys.argv[5]
with open(path) as f:
    releases = json.load(f)
row = {"name": title, "tag_name": tag, "body": notes}
if target:
    row["target"] = target
releases.append(row)
with open(path, "w") as f:
    json.dump(releases, f)
PY
		;;
	edit)
		tag=""
		notes=""
		while [[ $# -gt 0 ]]; do
			case "$1" in
			--repo) shift 2 ;;
			--notes)
				notes=$2
				shift 2
				;;
			--prerelease)
				shift
				;;
			*)
				if [[ -z $tag ]]; then
					tag=$1
				fi
				shift
				;;
			esac
		done
		python3 - "$FAKE_GH_STATE/releases.json" "$tag" "$notes" <<'PY'
import json, sys
path, tag, notes = sys.argv[1], sys.argv[2], sys.argv[3]
with open(path) as f:
    releases = json.load(f)
for r in releases:
    if r.get("tag_name") == tag:
        r["body"] = notes
        break
else:
    sys.exit("no release %s" % tag)
with open(path, "w") as f:
    json.dump(releases, f)
PY
		;;
	*)
		exit 2
		;;
	esac
	;;
*)
	exit 2
	;;
esac
EOF
chmod +x "$state/bin/gh"
export FAKE_GH_STATE=$state
export GH_PATH=$state/bin/gh

"$upsert" \
	--repo gonka-ai/gonka \
	--name "Devshard Release v5.0.0" \
	--tag devshard/v5.0.0 \
	--target abc \
	--body-line "devshardd v5 protocol v5 binary stamp v5.0.0" \
	--image ghcr.io/gonka-ai/versiond:0.2.15-devshard-v5 \
	--image ghcr.io/gonka-ai/versiond-router:0.2.15-devshard-v5 \
	|| fail "create failed"

python3 - "$state/releases.json" <<'PY' || fail "create body"
import json, sys
with open(sys.argv[1]) as f:
    rel = json.load(f)
assert len(rel) == 1, rel
body = rel[0]["body"]
assert "devshardd v5 protocol v5 binary stamp v5.0.0" in body
assert "## Images" in body
assert "ghcr.io/gonka-ai/versiond:0.2.15-devshard-v5" in body
assert "ghcr.io/gonka-ai/versiond-router:0.2.15-devshard-v5" in body
assert rel[0]["target"] == "abc"
assert body.count("## Images") == 1
PY

grep -q -- '--prerelease' "$state/argv.log" || fail "create should mark pre-release"

: >"$state/argv.log"
"$upsert" \
	--repo gonka-ai/gonka \
	--name "Devshard Release v5.0.0" \
	--tag devshard/v5.0.0 \
	--body-line "devshardd v5 protocol v5 binary stamp v5.0.0" \
	--image ghcr.io/gonka-ai/versiond:0.2.15-devshard-v5 \
	--image ghcr.io/gonka-ai/versiond-router:0.2.15-devshard-v5 \
	|| fail "reuse failed"

if grep -q 'release create' "$state/argv.log"; then
	fail "reuse must edit, not create"
fi
grep -q 'release edit' "$state/argv.log" || fail "reuse should edit notes"
grep -q -- '--prerelease' "$state/argv.log" || fail "reuse should mark pre-release"

python3 - "$state/releases.json" <<'PY' || fail "reuse must not duplicate Images"
import json, sys
with open(sys.argv[1]) as f:
    rel = json.load(f)
assert len(rel) == 1, rel
body = rel[0]["body"]
assert body.count("## Images") == 1, body
assert body.count("devshardd v5 protocol v5 binary stamp v5.0.0") == 1
PY

python3 -c '
import json, os, pathlib
p = pathlib.Path(os.environ["FAKE_GH_STATE"]) / "releases.json"
p.write_text(json.dumps([{"name": "Devshard Release v5.0.0", "tag_name": "devshard/v5.0.0", "body": "devshardd v5 protocol v5 binary stamp v5.0.0\n"}]))
'
: >"$state/argv.log"
"$upsert" \
	--repo gonka-ai/gonka \
	--name "Devshard Release v5.0.0" \
	--tag devshard/v5.0.0 \
	--body-line "devshardd v5 protocol v5 binary stamp v5.0.0" \
	--image ghcr.io/gonka-ai/versiond:0.2.15-devshard-v5 \
	|| fail "merge into zip-only notes failed"
python3 - "$state/releases.json" <<'PY' || fail "zip-only notes should gain Images"
import json, sys
with open(sys.argv[1]) as f:
    body = json.load(f)[0]["body"]
assert "devshardd v5 protocol v5 binary stamp v5.0.0" in body
assert "## Images" in body
assert "ghcr.io/gonka-ai/versiond:0.2.15-devshard-v5" in body
assert body.count("## Images") == 1
PY

printf 'devshard-release-upsert-notes_test: ok\n'
