#!/bin/sh
# Writes one file carrying the commit list, the stat summary and the full diff, and prints its path.
# With one argument the working tree is the far end, which is the usual case: review-package.sh HEAD.
# Files git does not track yet are included as new files, so an unstaged addition is reviewed too.
# With two arguments the range BASE..HEAD is. Reviewers read the file; the diff never enters the caller's context.
set -eu

if [ $# -lt 1 ]; then
	echo "usage: review-package.sh BASE [HEAD]" >&2
	exit 2
fi

base=$1
head=${2:-}
out="$(mktemp -d)/review-package.md"

{
	printf '# Review package: %s..%s\n\n## Commits\n\n' "$base" "${head:-working tree}"
	git log --oneline "$base..${head:-HEAD}" || true
	printf '\n## Files\n\n'
	# shellcheck disable=SC2086
	git diff --stat "$base" $head
	if [ -z "$head" ]; then
		git ls-files --others --exclude-standard | sed 's/^/ (untracked) /'
	fi
	printf '\n## Diff\n\n```diff\n'
	# shellcheck disable=SC2086
	git diff -U10 "$base" $head
	if [ -z "$head" ]; then
		git ls-files --others --exclude-standard | while IFS= read -r untracked; do
			git diff --no-index -U10 /dev/null "$untracked" || true
		done
	fi
	printf '```\n'
} > "$out"

echo "$out"
