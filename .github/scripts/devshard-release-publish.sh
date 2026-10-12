#!/usr/bin/env bash
# Create or reuse a named GitHub Release and clobber-upload assets.
# Requires `gh` on PATH (or GH_PATH). Uses GH_TOKEN / GH_HOST as gh does.
set -euo pipefail

usage() {
	cat >&2 <<'EOF'
Usage:
  devshard-release-publish.sh --repo OWNER/NAME --name TITLE --tag TAG
      [--target SHA] [--notes TEXT | --notes-file PATH] [--] ASSET [ASSET ...]
EOF
	exit 2
}

die() {
	printf 'devshard-release-publish: %s\n' "$*" >&2
	exit 1
}

repo=""
name=""
tag=""
target=""
notes=""
notes_file=""
assets=()

while [[ $# -gt 0 ]]; do
	case "$1" in
	--repo)
		repo=${2:-}
		shift 2
		;;
	--name)
		name=${2:-}
		shift 2
		;;
	--tag)
		tag=${2:-}
		shift 2
		;;
	--target)
		target=${2:-}
		shift 2
		;;
	--notes)
		notes=${2:-}
		shift 2
		;;
	--notes-file)
		notes_file=${2:-}
		shift 2
		;;
	--)
		shift
		assets+=("$@")
		break
		;;
	-h | --help)
		usage
		;;
	-*)
		die "unknown argument: $1"
		;;
	*)
		assets+=("$1")
		shift
		;;
	esac
done

[[ -n "$repo" && -n "$name" && -n "$tag" ]] || die "--repo, --name, and --tag are required"
[[ ${#assets[@]} -gt 0 ]] || die "at least one asset is required"
if [[ -n "$notes_file" ]]; then
	[[ -f "$notes_file" ]] || die "notes file not found: $notes_file"
	notes=$(cat "$notes_file")
fi
[[ -n "$notes" ]] || die "--notes or --notes-file is required"

gh_bin=${GH_PATH:-gh}
command -v "$gh_bin" >/dev/null || die "gh not found on PATH"

for asset in "${assets[@]}"; do
	[[ -f "$asset" ]] || die "asset not found: $asset"
done

existing_tag=""
existing_tag=$(
	"$gh_bin" api --paginate "repos/${repo}/releases" | python3 -c '
import json, sys
name = sys.argv[1]
releases = json.load(sys.stdin)
if not isinstance(releases, list):
    sys.exit("releases payload is not a list")
matches = [r for r in releases if r.get("name") == name]
if len(matches) > 1:
    sys.exit("multiple releases named %r" % name)
if not matches:
    sys.exit(0)
tag = matches[0].get("tag_name") or ""
print(tag)
' "$name"
)

if [[ -n "$existing_tag" ]]; then
	[[ $existing_tag == "$tag" ]] || die "release '$name' exists with tag '$existing_tag', expected '$tag'"
	printf 'devshard-release-publish: reusing existing release %s (tag %s)\n' "$name" "$tag" >&2
	"$gh_bin" release edit "$tag" --repo "$repo" --prerelease >/dev/null
else
	create_err=$(mktemp)
	create_args=("$tag" --repo "$repo" --title "$name" --notes "$notes" --prerelease)
	if [[ -n "$target" ]]; then
		create_args+=(--target "$target")
	fi
	if ! "$gh_bin" release create "${create_args[@]}" \
		>/dev/null 2>"$create_err"; then
		err=$(cat "$create_err")
		rm -f "$create_err"
		if [[ $err == *"already exists"* ]]; then
			printf 'devshard-release-publish: tag %s already existed; reusing\n' "$tag" >&2
			"$gh_bin" release edit "$tag" --repo "$repo" --prerelease >/dev/null
		else
			printf '%s\n' "$err" >&2
			die "failed to create release $tag"
		fi
	else
		rm -f "$create_err"
		printf 'devshard-release-publish: created release %s (tag %s)\n' "$name" "$tag" >&2
	fi
fi

"$gh_bin" release upload "$tag" \
	--repo "$repo" \
	--clobber \
	-- \
	"${assets[@]}"
printf 'devshard-release-publish: uploaded %d asset(s) to %s\n' "${#assets[@]}" "$tag" >&2
