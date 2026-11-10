#!/usr/bin/env bash
# Create or reuse a named GitHub Release and merge a ## Images section into
# its notes. Does not upload assets. Identity matches host zip publishes
# (name Devshard Release vX.Y.Z, tag devshard/vX.Y.Z).
set -euo pipefail

usage() {
	cat >&2 <<'EOF'
Usage:
  devshard-release-upsert-notes.sh --repo OWNER/NAME --name TITLE --tag TAG
      [--target SHA] [--body-line TEXT] --image URL [--image URL ...]
EOF
	exit 2
}

die() {
	printf 'devshard-release-upsert-notes: %s\n' "$*" >&2
	exit 1
}

repo=""
name=""
tag=""
target=""
body_line=""
images=()

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
	--body-line)
		body_line=${2:-}
		shift 2
		;;
	--image)
		[[ -n ${2:-} ]] || die "--image requires a URL"
		images+=("$2")
		shift 2
		;;
	-h | --help)
		usage
		;;
	*)
		die "unknown argument: $1"
		;;
	esac
done

[[ -n "$repo" && -n "$name" && -n "$tag" ]] || die "--repo, --name, and --tag are required"
[[ ${#images[@]} -gt 0 ]] || die "at least one --image is required"

gh_bin=${GH_PATH:-gh}
command -v "$gh_bin" >/dev/null || die "gh not found on PATH"
command -v python3 >/dev/null || die "python3 not found on PATH"

found_json=""
found_json=$(
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
print(json.dumps({"tag": matches[0].get("tag_name") or "", "body": matches[0].get("body") or ""}))
' "$name"
)

existing_tag=""
existing_body=""
if [[ -n "$found_json" ]]; then
	existing_tag=$(python3 -c 'import json,sys; print(json.loads(sys.argv[1]).get("tag",""))' "$found_json")
	existing_body=$(python3 -c 'import json,sys; print(json.loads(sys.argv[1]).get("body",""))' "$found_json")
	[[ $existing_tag == "$tag" ]] || die "release '$name' exists with tag '$existing_tag', expected '$tag'"
fi

notes=$(
	BODY_LINE="$body_line" EXISTING_BODY="$existing_body" IMAGES="$(printf '%s\n' "${images[@]}")" python3 <<'PY'
import os

body_line = os.environ.get("BODY_LINE", "")
existing = os.environ.get("EXISTING_BODY", "").replace("\r\n", "\n")
urls = [u for u in os.environ.get("IMAGES", "").split("\n") if u]
section = "## Images\n\n" + "\n".join("- `%s`" % u for u in urls)

body = existing.strip("\n")
if body_line:
    lines = body.splitlines()
    if body_line not in lines:
        body = body_line if not body else body_line + "\n\n" + body

marker = "## Images"
if "\n" + marker + "\n" in "\n" + body + "\n" or body.startswith(marker + "\n") or body == marker:
    prefix, _, rest = ("\n" + body).partition("\n" + marker)
    prefix = prefix.lstrip("\n")
    rest = rest.lstrip("\n")
    nxt = rest.find("\n## ")
    if nxt == -1:
        body = (prefix + "\n\n" + section).strip("\n") if prefix else section
    else:
        tail = rest[nxt + 1 :]
        body = ((prefix + "\n\n" + section + "\n\n" + tail) if prefix else section + "\n\n" + tail).strip("\n")
else:
    body = section if not body else body + "\n\n" + section
print(body.rstrip() + "\n")
PY
)

if [[ -n "$existing_tag" ]]; then
	"$gh_bin" release edit "$tag" --repo "$repo" --notes "$notes" --prerelease >/dev/null
	printf 'devshard-release-upsert-notes: updated notes on %s\n' "$tag" >&2
else
	create_args=("$tag" --repo "$repo" --title "$name" --notes "$notes" --prerelease)
	if [[ -n "$target" ]]; then
		create_args+=(--target "$target")
	fi
	create_err=$(mktemp)
	if ! "$gh_bin" release create "${create_args[@]}" >/dev/null 2>"$create_err"; then
		err=$(cat "$create_err")
		rm -f "$create_err"
		if [[ $err == *"already exists"* ]]; then
			"$gh_bin" release edit "$tag" --repo "$repo" --notes "$notes" --prerelease >/dev/null
			printf 'devshard-release-upsert-notes: tag existed; updated notes on %s\n' "$tag" >&2
		else
			printf '%s\n' "$err" >&2
			die "failed to create release $tag"
		fi
	else
		rm -f "$create_err"
		printf 'devshard-release-upsert-notes: created release %s (tag %s)\n' "$name" "$tag" >&2
	fi
fi
