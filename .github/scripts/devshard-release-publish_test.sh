#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
publish=$script_dir/devshard-release-publish.sh
chmod +x "$publish"

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

fail() {
	printf 'devshard-release-publish_test: %s\n' "$*" >&2
	exit 1
}

state=$tmpdir/gh
mkdir -p "$state/bin" "$state/assets"
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
	# Ignore --paginate and the path; tests always list the fixture.
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
			-*)
				shift
				[[ $# -gt 0 ]] && shift
				;;
			*)
				if [[ -z $tag ]]; then
					tag=$1
				fi
				shift
				;;
			esac
		done
		python3 - "$FAKE_GH_STATE/releases.json" "$title" "$tag" "$target" <<'PY'
import json, sys
path, title, tag, target = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
with open(path) as f:
    releases = json.load(f)
row = {"name": title, "tag_name": tag}
if target:
    row["target"] = target
releases.append(row)
with open(path, "w") as f:
    json.dump(releases, f)
PY
		printf 'created %s\n' "$tag"
		;;
	edit)
		# --prerelease on reuse; no fixture mutation needed beyond argv.log
		while [[ $# -gt 0 ]]; do
			shift
		done
		;;
	upload)
		tag=""
		files=()
		while [[ $# -gt 0 ]]; do
			case "$1" in
			--)
				shift
				;;
			--repo | --clobber)
				if [[ $1 == --repo ]]; then
					shift 2
				else
					shift
				fi
				;;
			-*)
				shift
				;;
			*)
				if [[ -z $tag ]]; then
					tag=$1
					shift
				else
					files+=("$1")
					shift
				fi
				;;
			esac
		done
		dest=$FAKE_GH_STATE/assets/$tag
		mkdir -p "$dest"
		for f in "${files[@]}"; do
			cp "$f" "$dest/$(basename "$f")"
		done
		;;
	*)
		printf 'fake gh: unknown release subcommand %s\n' "$sub" >&2
		exit 2
		;;
	esac
	;;
*)
	printf 'fake gh: unknown command %s\n' "$cmd" >&2
	exit 2
	;;
esac
EOF
chmod +x "$state/bin/gh"

export FAKE_GH_STATE=$state
export PATH="$state/bin:$PATH"
export GH_PATH=$state/bin/gh

zip1=$tmpdir/a/devshardd.zip
sha1=$tmpdir/a/devshardd.zip.sha256
mkdir -p "$tmpdir/a"
printf 'zip-v1' >"$zip1"
printf 'sha-v1' >"$sha1"

"$publish" \
	--repo gonka-ai/gonka \
	--name "Devshard Release v4.1.0" \
	--tag devshard/v4.1.0 \
	--target abcdef \
	--notes "devshardd v4.1 protocol v4 binary stamp v4.1.0" \
	-- \
	"$zip1" "$sha1" || fail "create+upload failed"

python3 - "$state/releases.json" <<'PY' || fail "expected one release after create"
import json, sys
with open(sys.argv[1]) as f:
    releases = json.load(f)
assert len(releases) == 1, releases
assert releases[0]["name"] == "Devshard Release v4.1.0"
assert releases[0]["tag_name"] == "devshard/v4.1.0"
assert releases[0]["target"] == "abcdef"
PY

asset_dir=$state/assets/devshard/v4.1.0
[[ $(cat "$asset_dir/devshardd.zip") == zip-v1 ]] || fail "uploaded zip mismatch"
[[ $(cat "$asset_dir/devshardd.zip.sha256") == sha-v1 ]] || fail "uploaded sha mismatch"
[[ ! -e $asset_dir/Source\ code.zip ]] || fail "must not upload GitHub source archives"

if grep -q 'release create' "$state/argv.log"; then
	:
else
	fail "expected gh release create on first publish"
fi
grep -q -- '--target abcdef' "$state/argv.log" || fail "create should pass --target"
grep -q -- '--prerelease' "$state/argv.log" || fail "create should mark pre-release"

: >"$state/argv.log"
printf 'zip-v2' >"$zip1"
"$publish" \
	--repo gonka-ai/gonka \
	--name "Devshard Release v4.1.0" \
	--tag devshard/v4.1.0 \
	--notes "should not rewrite" \
	-- \
	"$zip1" "$sha1" || fail "reuse+clobber failed"

if grep -q 'release create' "$state/argv.log"; then
	fail "must not create when the name already exists"
fi
grep -q -- '--prerelease' "$state/argv.log" || fail "reuse should keep pre-release"
python3 - "$state/releases.json" <<'PY' || fail "reuse must not add a second release"
import json, sys
with open(sys.argv[1]) as f:
    releases = json.load(f)
assert len(releases) == 1, releases
PY
[[ $(cat "$asset_dir/devshardd.zip") == zip-v2 ]] || fail "clobber did not replace zip bytes"

printf '[]\n' >"$state/releases.json"
python3 -c '
import json, os, pathlib
p = pathlib.Path(os.environ["FAKE_GH_STATE"]) / "releases.json"
p.write_text(json.dumps([{"name": "Devshard Release v4.1.0", "tag_name": "other/v4.1.0"}]))
'

if "$publish" \
	--repo gonka-ai/gonka \
	--name "Devshard Release v4.1.0" \
	--tag devshard/v4.1.0 \
	--notes "nope" \
	-- \
	"$zip1" "$sha1"; then
	fail "mismatched tag_name should fail"
fi

printf 'devshard-release-publish_test: ok\n'
