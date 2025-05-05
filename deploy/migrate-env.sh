#!/usr/bin/env sh
# migrate-env.sh – portable env-file migration
# Works under any POSIX shell (dash, busybox ash, zsh, bash).
#
# Usage:   ./migrate-env.sh <old_config.env> <new_config.env>
# The script copies/renames variables listed in MIGRATE_ENVS / RENAMES from
# the old file into the new one. It preserves the *original line order* of
# <new_config.env>. When a variable is renamed, its old line is replaced in‑place
# with the new name, so ordering remains unchanged.
# A timestamped backup of <new_config.env> is created automatically.

set -eu

usage() { echo "Usage: $0 <old_config.env> <new_config.env>" >&2; exit 1; }
[ $# -eq 2 ] || usage
OLD="$1"; NEW="$2"
[ -r "$OLD" ] || { echo "Cannot read $OLD" >&2; exit 2; }
[ -r "$NEW" ] || { echo "Cannot read $NEW" >&2; exit 2; }

BACKUP="${NEW}.bak.$(date +%s)"; cp "$NEW" "$BACKUP"
printf '[info] Backup of %s saved to %s\n' "$NEW" "$BACKUP" >&2

# ───── EDIT BELOW ────────────────────────────────────────────────────────────
MIGRATE_ENVS="\
PORT
KEY_NAME
PUBLIC_URL
P2P_EXTERNAL_ADDRESS
HF_HOME
SEED_API_URL
SEED_NODE_RPC_URL
SEED_NODE_P2P_URL
"

RENAMES="\
PORT=API_PORT
"
# ─────────────────────────────────────────────────────────────────────────────

trim() { printf '%s' "$1" | awk '{gsub(/^ +| +$/,"",$0);print}'; }

lookup_rename() {
  var="$1"; for pair in $RENAMES; do case $pair in \#*) continue;; esac
    old="${pair%%=*}"; new="${pair#*=}"; [ "$old" = "$var" ] && { echo "$new"; return; }
  done; echo "$var";
}

exists_var() { var="$1"; file="$2"; grep -q -E "^(export[[:space:]]+)?$var=" "$file"; }

get_value() {
  var="$1"; file="$2"
  grep -E "^(export[[:space:]]+)?$var=" "$file" | head -n1 | cut -d= -f2-
}

# Replace first occurrence of OLD with NEW=VAL, preserving order
replace_line() {
  oldvar="$1"; newvar="$2"; value="$3"; file="$4"; tmp="$(mktemp)"
  awk -v OLD="$oldvar" -v NEW="$newvar" -v VAL="$value" '
    BEGIN{done=0; regex="^(export[[:space:]]+)?"OLD"="}
    $0 ~ regex && !done {print "export "NEW"="VAL; done=1; next}
    {print}
  ' "$file" > "$tmp" && mv "$tmp" "$file"
}

# Update the value of VAR=... in place
update_value() {
  var="$1"; val="$2"; file="$3"; tmp="$(mktemp)"
  awk -v V="$var" -v VAL="$val" '
    BEGIN{done=0; regex="^(export[[:space:]]+)?"V"="}
    $0 ~ regex && !done {print "export "V"="VAL; done=1; next}
    {print}
  ' "$file" > "$tmp" && mv "$tmp" "$file"
}

append_var() { var="$1"; val="$2"; file="$3"; printf '\nexport %s=%s\n' "$var" "$val" >> "$file"; }

for VAR in $MIGRATE_ENVS; do VAR="$(trim "$VAR")"; [ -n "$VAR" ] || continue
  VAL="$(get_value "$VAR" "$OLD" || true)"; [ -n "$VAL" ] || continue
  TARGET="$(lookup_rename "$VAR")"

  if [ "$TARGET" != "$VAR" ]; then
    if exists_var "$VAR" "$NEW"; then
      replace_line "$VAR" "$TARGET" "$VAL" "$NEW"
      printf '[info] Renamed %s → %s (kept order)\n' "$VAR" "$TARGET" >&2
    elif exists_var "$TARGET" "$NEW"; then
      update_value "$TARGET" "$VAL" "$NEW"
      printf '[info] Updated existing %s\n' "$TARGET" >&2
    else
      append_var "$TARGET" "$VAL" "$NEW"
      printf '[info] Added %s at end\n' "$TARGET" >&2
    fi
  else
    if exists_var "$TARGET" "$NEW"; then
      update_value "$TARGET" "$VAL" "$NEW"
      printf '[info] Updated %s\n' "$TARGET" >&2
    else
      append_var "$TARGET" "$VAL" "$NEW"
      printf '[info] Added %s at end\n' "$TARGET" >&2
    fi
  fi
done

printf '[info] Migration complete.\n' >&2
