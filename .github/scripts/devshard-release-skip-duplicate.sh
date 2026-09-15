#!/usr/bin/env bash
# First-wins skip for host publishes. GitHub has no "skip if another run is
# already busy". If an older queued/in-progress run of this workflow is already
# publishing the same github.sha or the same leftover release tag, print
# skip=true and exit 0 so the job can succeed without rebuilding.
set -euo pipefail

usage() {
	cat >&2 <<'EOF'
Usage:
  devshard-release-skip-duplicate.sh --run-id ID --sha SHA --release-tag TAG
      [--repo OWNER/NAME] [--workflow FILE-OR-NAME]
EOF
	exit 2
}

die() {
	printf 'devshard-release-skip-duplicate: %s\n' "$*" >&2
	exit 1
}

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
run_id=""
sha=""
release_tag=""
repo=""
workflow="publish_upgrade_binaries.yml"

while [[ $# -gt 0 ]]; do
	case "$1" in
	--run-id)
		run_id=${2:-}
		shift 2
		;;
	--sha)
		sha=${2:-}
		shift 2
		;;
	--release-tag)
		release_tag=${2:-}
		shift 2
		;;
	--repo)
		repo=${2:-}
		shift 2
		;;
	--workflow)
		workflow=${2:-}
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

[[ -n "$run_id" && -n "$sha" && -n "$release_tag" ]] || die "--run-id, --sha, and --release-tag are required"

gh_bin=${GH_PATH:-gh}
meta=${META_PATH:-$script_dir/devshard-release-meta.sh}
command -v "$gh_bin" >/dev/null || die "gh not found on PATH"
[[ -x "$meta" ]] || die "meta helper is not executable: $meta"
command -v python3 >/dev/null || die "python3 not found on PATH"

list_args=()
if [[ -n $repo ]]; then
	list_args+=(--repo "$repo")
fi
list_args+=(--workflow "$workflow" --limit 50 --json "databaseId,headSha,headBranch")

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

# requested/waiting/pending cover runs that are accepted but not yet in_progress.
statuses=(queued in_progress requested waiting pending)
paths=()
for status in "${statuses[@]}"; do
	out=$tmpdir/$status.json
	"$gh_bin" run list "${list_args[@]}" --status "$status" >"$out"
	paths+=("$out")
done

python3 - "$run_id" "$sha" "$release_tag" "$meta" "${paths[@]}" <<'PY'
import json, subprocess, sys

run_id = int(sys.argv[1])
sha = sys.argv[2]
release_tag = sys.argv[3]
meta = sys.argv[4]
paths = sys.argv[5:]


def leftover(branch: str) -> str:
    proc = subprocess.run(
        [meta, "leftover-from-ref", "--ref-name", branch],
        capture_output=True,
        text=True,
        check=False,
    )
    if proc.returncode != 0:
        return ""
    return proc.stdout.strip()


runs = []
for path in paths:
    with open(path, encoding="utf-8") as f:
        payload = json.load(f)
    if not isinstance(payload, list):
        sys.exit("run list payload is not a list")
    runs.extend(payload)

reason = ""
for run in runs:
    other_id = int(run.get("databaseId") or 0)
    if other_id == 0 or other_id == run_id or other_id >= run_id:
        continue
    branch = run.get("headBranch") or ""
    lo = leftover(branch)
    if not lo:
        continue
    other_sha = run.get("headSha") or ""
    other_tag = "devshard/" + lo
    if other_sha == sha or other_tag == release_tag:
        reason = (
            f"older run {other_id} already publishing "
            f"(sha={other_sha} branch={branch} leftover={lo})"
        )
        break

if reason:
    print("skip=true")
    print("reason=" + reason)
else:
    print("skip=false")
    print("reason=")
PY
