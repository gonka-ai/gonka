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
}

socket_path=/var/run/haproxy/reconciler.sock
cli_in=/tmp/h2-watch-drain.in
cli_out=/tmp/h2-watch-drain.out
cli_ready=0

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

# Master-worker soft-stop closes the listening stats socket. A CLI connection
# opened before that signal still accepts show and shutdown session.
open_cli() {
    rm -f "$cli_in" "$cli_out"
    mkfifo "$cli_in"
    : >"$cli_out"
    socat -t 7200 - "$socket_path" <"$cli_in" >"$cli_out" &
    cli_socat=$!
    exec 3>"$cli_in"
    printf 'prompt\n' >&3
    i=0
    while [ "$i" -lt 50 ]; do
        if grep -q '^> *$' "$cli_out" 2>/dev/null; then
            cli_ready=1
            return 0
        fi
        i=$((i + 1))
        sleep 0.05
    done
    exec 3>&-
    kill "$cli_socat" 2>/dev/null || true
    return 1
}

close_cli() {
    [ "$cli_ready" -eq 1 ] || return 0
    cli_ready=0
    exec 3>&-
    kill "$cli_socat" 2>/dev/null || true
}

cli_query() {
    cmd=$1
    dest=$2
    start=$(wc -c <"$cli_out" | awk '{print $1}')
    printf '%s\n' "$cmd" >&3
    i=0
    while [ "$i" -lt 100 ]; do
        tail -c +$((start + 1)) "$cli_out" >"$cli_out.chunk" 2>/dev/null || true
        if grep -q '^> *$' "$cli_out.chunk" 2>/dev/null; then
            grep -v '^> *$' "$cli_out.chunk" | sed 's/^> //' >"$dest"
            return 0
        fi
        i=$((i + 1))
        sleep 0.05
    done
    return 1
}

# Session ids that belong to an HTTP/2 connection. Soft-stop cannot finish
# while any of these, or the CLI connection itself, is still open.
h2_session_ids() {
    awk '
        function flush() {
            if (have && h2) print id
        }
        /^0x[0-9a-fA-F]+:/ {
            flush()
            have = 1
            id = $1
            sub(/:$/, "", id)
            h2 = index($0, "h2c=") > 0
            next
        }
        {
            if (index($0, "h2c=")) h2 = 1
        }
        END { flush() }
    ' "$1"
}

release_watches() {
    [ "$cli_ready" -eq 1 ] || return 0
    table=$(mktemp)
    sess=$(mktemp)
    ids_file=$(mktemp)
    if ! cli_query 'show table h2_stream_acct' "$table"; then
        rm -f "$table" "$sess" "$ids_file"
        return 0
    fi
    if ! cli_query 'show sess all' "$sess"; then
        rm -f "$table" "$sess" "$ids_file"
        return 0
    fi
    select_idle_watch_sessions "$table" "$sess" >"$ids_file" || true
    h2_ids=$(h2_session_ids "$sess")
    rm -f "$table" "$sess"
    # Close the CLI once every HTTP/2 session in this snapshot is idle.
    # An in-flight inference is an h2 session that is not in the idle set,
    # and that connection has to stay up until its response is flushed.
    close_after=1
    for id in $h2_ids; do
        if ! grep -qx "$id" "$ids_file"; then
            close_after=0
            break
        fi
    done
    if [ -s "$ids_file" ]; then
        while IFS= read -r id; do
            case $id in
                0x*) ;;
                *) continue ;;
            esac
            cli_query "shutdown session $id" "$cli_out.ack" || true
            log "h2-watch-drain: closed idle watch session $id"
        done <"$ids_file"
    fi
    rm -f "$ids_file"
    if [ "$close_after" -eq 1 ]; then
        close_cli
    fi
}

wait_for_socket() {
    i=0
    while [ "$i" -lt 50 ]; do
        if printf 'show info\n' | socat -t 1 stdio "$socket_path" 2>/dev/null | grep -q '^Name:'; then
            return 0
        fi
        kill -0 "$1" 2>/dev/null || return 1
        i=$((i + 1))
        sleep 0.2
    done
    return 1
}

supervise() {
    bin=$1
    cfg=$2
    stopping=0
    signaled=0
    hard=0
    trap 'stopping=1' USR1
    trap 'stopping=1; hard=1' TERM INT
    "$bin" -W -db -f "$cfg" &
    pid=$!
    if wait_for_socket "$pid"; then
        open_cli || log "h2-watch-drain: runtime CLI did not stay open"
    fi
    while kill -0 "$pid" 2>/dev/null; do
        if [ "$hard" -eq 1 ]; then
            kill -TERM "$pid" 2>/dev/null || true
        elif [ "$stopping" -eq 1 ] && [ "$signaled" -eq 0 ]; then
            log "h2-watch-drain: soft-stop"
            kill -USR1 "$pid" 2>/dev/null || true
            signaled=1
        fi
        if [ "$signaled" -eq 1 ]; then
            release_watches || true
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
        echo "h2-watch-drain: use --supervise or --select" >&2
        exit 2
        ;;
esac
