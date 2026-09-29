#!/bin/sh
# Closes Watch streams when the router soft-stops.
#
# Watch and Chat share one HTTP/2 connection, and soft-stop waits for that
# connection. Each Watch request records its stream id in stick table
# h2_watch_ids. This helper shuts down only those streams. A Chat stream is
# left alone until its own response finishes. HAProxy evaluates
# http-after-response when the response headers are ready, before the body,
# so a header-time counter is not used to decide which streams to close.
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

# Print HTTP/2 session pointers whose stream id was recorded as a Watch.
# Chat sessions on the same connection are not selected. The id compared
# here is the "id=" field, not a substring of "h2s.id=".
select_watch_sessions() {
    awk '
        function field_prefix(name,    i, p) {
            p = name "="
            for (i = 1; i <= NF; i++)
                if (index($i, p) == 1)
                    return substr($i, length(p) + 1)
            return ""
        }
        FNR == NR {
            if ($0 ~ /^#/) next
            key = field_prefix("key")
            if (key != "") watch[key] = 1
            next
        }
        function flush() {
            if (have && h2 && sid != "" && (sid in watch)) print ptr
        }
        /^0x[0-9a-fA-F]+:/ {
            flush()
            have = 1
            ptr = $1
            sub(/:$/, "", ptr)
            sid = field_prefix("id")
            h2 = index($0, "h2c=") > 0
            next
        }
        {
            if (index($0, "h2c=")) h2 = 1
        }
        END { flush() }
    ' "$1" "$2"
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
    rm -f "$table" "$sess" "$ids_file"
}

release_watches() {
    # cli_done means every Watch in the previous snapshot was shut down and
    # the CLI closed. Chat streams may still be running. Anything else with
    # no live CLI is an unfinished drain.
    [ "$cli_done" -eq 1 ] && return 0
    [ "$cli_ready" -eq 1 ] || return 1
    table=$(mktemp)
    sess=$(mktemp)
    ids_file=$(mktemp)
    if ! cli_query 'show table h2_watch_ids' "$table"; then
        note_cli_down
        return 1
    fi
    if ! cli_query 'show sess all' "$sess"; then
        note_cli_down
        return 1
    fi
    select_watch_sessions "$table" "$sess" >"$ids_file" || true
    rm -f "$table" "$sess"
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
        [ "$#" -eq 3 ] || exit 2
        select_watch_sessions "$2" "$3"
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
