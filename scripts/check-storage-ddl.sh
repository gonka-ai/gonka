#!/usr/bin/env bash
# Storage schema DDL guard for devshard session storage only.
#
# Pass 1 — hot-path / parent DDL location:
#   CREATE TABLE and CREATE INDEX must not appear in store implementation code.
#   Allowed locations:
#     - *_migrate.go and migrate.go (ordered migrations per package)
#     - devshard/storage/migrate/ (shared migrate framework bootstrap)
#   Exceptions in non-migrate files:
#     - Lines that are Go comments (// ...)
#     - Lazy per-epoch partition DDL containing "PARTITION OF" in
#       devshard/storage/postgres.go
#
# Pass 2 — forward-only migrations:
#   Inside *_migrate.go and devshard/storage/migrate/*.go, disallow destructive
#   or narrowing schema changes: DROP, RENAME, ALTER COLUMN (ADD COLUMN is OK).
#
# Run from repo root: bash scripts/check-storage-ddl.sh

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if ! command -v rg >/dev/null 2>&1; then
	echo "error: ripgrep (rg) is required" >&2
	exit 1
fi

# Devshard session storage only (not dapi gonka.db, stats, or payloadstorage).
STORAGE_DIRS=(
	devshard/storage
)

violations=0

fail() {
	echo "check-storage-ddl: $*" >&2
	violations=1
}

# --- Pass 1: parent DDL only in migrate files (plus partition exception) ---
while IFS= read -r line; do
	[[ -z "$line" ]] && continue
	file="${line%%:*}"
	rest="${line#*:}"
	lineno="${rest%%:*}"
	content="${rest#*:}"

	trimmed="${content#"${content%%[![:space:]]*}"}"
	if [[ "$trimmed" == //* ]]; then
		continue
	fi

	case "$file" in
		devshard/storage/postgres.go)
			if [[ "$content" == *PARTITION\ OF* ]]; then
				continue
			fi
			;;
	esac

	fail "pass 1: CREATE TABLE/INDEX outside migrate files: ${file}:${lineno}:${content}"
done < <(rg -n 'CREATE (TABLE|INDEX)' \
	"${STORAGE_DIRS[@]}" \
	--glob '*.go' \
	--glob '!*_migrate.go' \
	--glob '!migrate.go' \
	--glob '!*_test.go' \
	--glob '!**/migrate/**' \
	2>/dev/null || true)

# --- Pass 2: no destructive migration keywords in migrate sources only ---
MIGRATE_GLOBS=('*_migrate.go' 'migrate.go')
while IFS= read -r line; do
	[[ -z "$line" ]] && continue
	file="${line%%:*}"
	rest="${line#*:}"
	content="${rest#*:}"
	trimmed="${content#"${content%%[![:space:]]*}"}"
	if [[ "$trimmed" == //* ]]; then
		continue
	fi
	fail "pass 2: destructive migration keyword: $line"
done < <(rg -n -i 'DROP TABLE|DROP COLUMN|DROP INDEX|RENAME TABLE|RENAME COLUMN|ALTER COLUMN' \
	"${STORAGE_DIRS[@]}" \
	devshard/storage/migrate \
	$(printf -- '--glob %s ' "${MIGRATE_GLOBS[@]}") \
	2>/dev/null || true)

if [[ "$violations" -ne 0 ]]; then
	echo "check-storage-ddl: failed" >&2
	exit 1
fi

echo "check-storage-ddl: ok"
