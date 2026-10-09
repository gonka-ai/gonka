#!/usr/bin/env bash
# Exit 0 if GHCR already has OWNER/PACKAGE:TAG. Exit 1 if missing.
# Uses the GitHub Packages API (GH_TOKEN). OWNER_TYPE is Organization or User.
set -euo pipefail

usage() {
	cat >&2 <<'EOF'
Usage:
  devshard-ghcr-tag-exists.sh --owner OWNER --package NAME --tag TAG
      [--owner-type Organization|User]
EOF
	exit 2
}

die() {
	printf 'devshard-ghcr-tag-exists: %s\n' "$*" >&2
	exit 1
}

owner=""
package=""
tag=""
owner_type="Organization"

while [[ $# -gt 0 ]]; do
	case "$1" in
	--owner)
		owner=${2:-}
		shift 2
		;;
	--package)
		package=${2:-}
		shift 2
		;;
	--tag)
		tag=${2:-}
		shift 2
		;;
	--owner-type)
		owner_type=${2:-}
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

[[ -n "$owner" && -n "$package" && -n "$tag" ]] || die "--owner, --package, and --tag are required"

gh_bin=${GH_PATH:-gh}
command -v "$gh_bin" >/dev/null || die "gh not found on PATH"
command -v python3 >/dev/null || die "python3 not found on PATH"

case "$owner_type" in
Organization | organization) api_root="orgs/${owner}" ;;
User | user) api_root="users/${owner}" ;;
*) die "unknown --owner-type $owner_type" ;;
esac

err=$(mktemp)
if ! "$gh_bin" api --paginate "${api_root}/packages/container/${package}/versions" >"$err" 2>/dev/null; then
	rm -f "$err"
	exit 1
fi
python3 - "$tag" "$err" <<'PY'
import json, sys
tag, path = sys.argv[1], sys.argv[2]
with open(path, encoding="utf-8") as f:
    payload = json.load(f)
if not isinstance(payload, list):
    sys.exit(1)
for ver in payload:
    tags = (ver.get("metadata") or {}).get("container", {}).get("tags") or []
    if tag in tags:
        sys.exit(0)
sys.exit(1)
PY
rc=$?
rm -f "$err"
exit "$rc"
