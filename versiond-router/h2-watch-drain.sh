#!/bin/sh
# Closes Watch streams on HTTP/2 connections whose inferences have finished.
#
# HAProxy soft-stop waits for the connection. Watch and inference share one,
# and Watch stays open after the inference response has been sent. The frontend
# counts non-Watch streams in stick table h2_stream_acct: gpc0 starts, gpc1
# finishes. Equal counts mean nothing is left in flight (an aborted inference
# never reaches the response rule, so gpc0 stays ahead and the Watch is kept).
# Remaining streams on that client address are Watch, and shutdown session
# ends them so the soft-stop can finish.
set -eu

log() {
    echo "$*" >&2
    if [ -n "${H2_WATCH_DRAIN_LOG:-}" ]; then
        printf '%s\n' "$*" >>"$H2_WATCH_DRAIN_LOG"
    fi
}

socket_path=/var/run/haproxy/reconciler.sock

select_idle_watch_sessions() {
    awk '
        function field_after(tag,    n, rest, parts) {
            n = index($0, tag)
            if (!n) return ""
            rest = substr($0, n + length(tag))
            split(rest, parts, " ")
            return parts[1]
        }
        function gpc(name,    raw) {
            raw = field_after(name "=")
            if (raw == "") return -1
            return raw + 0
        }
        FNR == NR {
            key = field_after("key=")
            if (key == "") next
            g0 = gpc("gpc0")
            g1 = gpc("gpc1")
            if (g0 >= 0 && g0 == g1) idle[key] = 1
            next
        }
        function flush() {
            if (have && h2 && source != "" && (source in idle)) print id
        }
        /^0x[0-9a-fA-F]+:/ {
            flush()
            have = 1
            id = $1
            sub(/:$/, "", id)
            source = field_after("source=")
            h2 = index($0, "h2c=") > 0
            next
        }
        {
            if (index($0, "h2c=")) h2 = 1
            if (source == "") source = field_after("source=")
        }
        END { flush() }
    ' "$1" "$2"
}

release_watches() {
    sock=$1
    table=$(mktemp)
    sess=$(mktemp)
    if ! printf '%s\n' 'show table h2_stream_acct' | socat -t 1 stdio "$sock" >"$table" 2>/dev/null; then
        rm -f "$table" "$sess"
        return 0
    fi
    if ! printf '%s\n' 'show sess all' | socat -t 1 stdio "$sock" >"$sess" 2>/dev/null; then
        rm -f "$table" "$sess"
        return 0
    fi
    ids=$(select_idle_watch_sessions "$table" "$sess" || true)
    rm -f "$table" "$sess"
    [ -n "$ids" ] || return 0
    log "h2-watch-drain: closing idle watch sessions"
    printf '%s\n' "$ids" | while IFS= read -r id; do
        case $id in
            0x*) ;;
            *) continue ;;
        esac
        printf 'shutdown session %s\n' "$id" | socat -t 1 stdio "$sock" >/dev/null 2>&1 || true
        log "h2-watch-drain: closed idle watch session $id"
    done
}

supervise() {
    bin=$1
    cfg=$2
    "$bin" -W -db -f "$cfg" &
    pid=$!
    stopping=0
    trap 'stopping=1; log "h2-watch-drain: soft-stop"; kill -USR1 "$pid" 2>/dev/null || true' USR1
    trap 'stopping=1; kill -TERM "$pid" 2>/dev/null || true' TERM INT
    while kill -0 "$pid" 2>/dev/null; do
        if [ "$stopping" -eq 1 ]; then
            release_watches "$socket_path" || true
        fi
        sleep 0.2
    done
    wait "$pid"
}

case ${1:-} in
    --select)
        [ "$#" -eq 3 ] || exit 2
        select_idle_watch_sessions "$2" "$3"
        ;;
    --supervise)
        [ "$#" -eq 3 ] || exit 2
        supervise "$2" "$3"
        ;;
    *)
        release_watches "${1:-$socket_path}"
        ;;
esac
