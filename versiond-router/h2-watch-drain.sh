#!/bin/sh
# Closes Watch streams when the router soft-stops.
#
# Watch and Chat share one HTTP/2 connection, and soft-stop waits for that
# connection. option httplog keeps each stream's request line until that
# stream ends. "show sess all show-uri" prints it. This helper shuts down
# HTTP/2 streams whose request line is PeerAuthService/Watch. A Chat stream
# has a different line and is left alone until its own response finishes.
# HAProxy does not print an http-request capture in "show sess", and it has
# no end-of-body rule, so neither a capture nor a stick table is used.
#
# The runtime CLI is opened when SIGUSR1 arrives, and only then is SIGUSR1
# forwarded to HAProxy. Soft-stop closes the listening stats socket, so the
# session has to exist before that forward. It does not have to exist from
# process start: `stats timeout` drops an idle CLI, and a router outlives any
# finite override of that timer. The drain polls inside the timeout.
set -eu

log() {
    echo "$*" >&2
}

socket_path=/var/run/haproxy/reconciler.sock
cli_in=/tmp/h2-watch-drain.in
cli_out=/tmp/h2-watch-drain.out
cli_ready=0
cli_done=0
cli_warned=0
cli_socat=

# Print HTTP/2 session pointers whose request line is a Watch.
# "show sess all show-uri" puts that line on the txn field as
# uri="METHOD target HTTP/x.y". Chat on the same connection has a different
# line. An HTTP/1.1 stream is not selected: h2c=0x is the mux pointer, and
# a path cannot spoof that prefix. The numeric id= is not used.
select_watch_sessions() {
    awk '
        function flush() {
            if (have && h2 && watch) print ptr
        }
        /^0x[0-9a-fA-F]+:/ {
            flush()
            have = 1
            ptr = $1
            sub(/:$/, "", ptr)
            h2 = index($0, "h2c=0x") > 0
            watch = index($0, "PeerAuthService/Watch HTTP/") > 0
            next
        }
        {
            if (index($0, "h2c=0x")) h2 = 1
            if (index($0, "PeerAuthService/Watch HTTP/")) watch = 1
        }
        END { flush() }
    ' "$1"
}

# Drop a half-open CLI. The listening socket is still up until SIGUSR1 is
# forwarded, so the caller can try again. A write-only fifo open would block
# until socat connects; O_RDWR returns immediately on Linux, which keeps a
# missing socket from stalling the supervisor for the whole stop grace.
abandon_cli() {
    cli_ready=0
    # Bare `exec` applies every redirection to this shell. Do not attach
    # `2>/dev/null` here: it would discard later drain logs.
    exec 3>&- || true
    if [ -n "$cli_socat" ]; then
        kill "$cli_socat" 2>/dev/null || true
        wait "$cli_socat" 2>/dev/null || true
        cli_socat=
    fi
    rm -f "$cli_in" "$cli_out" "$cli_out.ack" "$cli_out.chunk"
}

open_cli() {
    rm -f "$cli_in" "$cli_out" "$cli_out.ack" "$cli_out.chunk"
    mkfifo "$cli_in"
    : >"$cli_out"
    exec 3<>"$cli_in"
    socat -t 1 - "$socket_path" <"$cli_in" >"$cli_out" 2>/dev/null &
    cli_socat=$!
    if ! printf 'prompt\n' >&3; then
        abandon_cli
        return 1
    fi
    i=0
    while [ "$i" -lt 50 ]; do
        if ! kill -0 "$cli_socat" 2>/dev/null; then
            abandon_cli
            return 1
        fi
        if grep -q '^> *$' "$cli_out" 2>/dev/null; then
            if cli_query 'show info' "$cli_out.ack" && grep -q '^Name:' "$cli_out.ack"; then
                cli_ready=1
                return 0
            fi
            abandon_cli
            return 1
        fi
        i=$((i + 1))
        sleep 0.05
    done
    abandon_cli
    return 1
}

close_cli() {
    [ "$cli_ready" -eq 1 ] || return 0
    cli_ready=0
    cli_done=1
    exec 3>&- || true
    if [ -n "$cli_socat" ]; then
        kill "$cli_socat" 2>/dev/null || true
        wait "$cli_socat" 2>/dev/null || true
        cli_socat=
    fi
}

cli_query() {
    cmd=$1
    dest=$2
    start=$(wc -c <"$cli_out" | awk '{print $1}')
    # SIGPIPE would kill the supervisor. A dead CLI fails the query instead.
    printf '%s\n' "$cmd" >&3 || return 1
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

note_cli_down() {
    if [ "$cli_warned" -eq 0 ]; then
        log "h2-watch-drain: runtime CLI did not answer"
        cli_warned=1
    fi
    rm -f "$sess" "$ids_file"
}

release_watches() {
    # cli_done means every Watch in the previous snapshot was shut down and
    # the CLI closed. Chat streams may still be running. Anything else with
    # no live CLI is an unfinished drain.
    [ "$cli_done" -eq 1 ] && return 0
    [ "$cli_ready" -eq 1 ] || return 1
    sess=$(mktemp)
    ids_file=$(mktemp)
    if ! cli_query 'show sess all show-uri' "$sess"; then
        note_cli_down
        return 1
    fi
    select_watch_sessions "$sess" >"$ids_file" || true
    rm -f "$sess"
    if [ -s "$ids_file" ]; then
        while IFS= read -r id; do
            case $id in
                0x*) ;;
                *) continue ;;
            esac
            cli_query "shutdown session $id" "$cli_out.ack" || true
            log "h2-watch-drain: closed watch session $id"
        done <"$ids_file"
        rm -f "$ids_file"
        return 0
    fi
    rm -f "$ids_file"
    # No Watch left. The CLI itself would hold soft-stop; Chat streams keep
    # the process up until their bodies finish.
    close_cli
}

supervise() {
    bin=$1
    cfg=$2
    stopping=0
    signaled=0
    hard=0
    cli_hold_logged=0
    # A dead CLI must not kill this process; Docker would then skip the drain.
    trap '' PIPE
    trap 'stopping=1' USR1
    trap 'stopping=1; hard=1' TERM INT
    # Master-worker reload and HUP are HAProxy's, not a stop. A supervisor in
    # front of this script forwards them here; pass them on to the master.
    trap 'kill -USR2 "$pid" 2>/dev/null || true' USR2
    trap 'kill -HUP "$pid" 2>/dev/null || true' HUP
    "$bin" -W -db -f "$cfg" &
    pid=$!
    while kill -0 "$pid" 2>/dev/null; do
        if [ "$hard" -eq 1 ]; then
            kill -TERM "$pid" 2>/dev/null || true
        elif [ "$stopping" -eq 1 ] && [ "$signaled" -eq 0 ]; then
            if open_cli; then
                log "h2-watch-drain: soft-stop"
                kill -USR1 "$pid" 2>/dev/null || true
                signaled=1
            elif [ "$cli_hold_logged" -eq 0 ]; then
                log "h2-watch-drain: runtime CLI did not open; soft-stop held"
                cli_hold_logged=1
            fi
        fi
        if [ "$signaled" -eq 1 ]; then
            # A failed query is not a finished drain. Soft-stop has closed the
            # listening socket, so this session is the only one we can use.
            release_watches || true
        fi
        sleep 0.2
    done
    wait "$pid"
}

case ${1:-} in
    --select)
        [ "$#" -eq 2 ] || exit 2
        select_watch_sessions "$2"
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
