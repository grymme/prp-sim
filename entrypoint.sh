#!/bin/sh
# entrypoint.sh — manages telnetd and prpd lifecycle
#
# Signals:
#   SIGHUP  — reload config: restart prpd (kill existing, start new)
#   SIGTERM — graceful shutdown of all child processes
#   SIGINT  — same as SIGTERM (Ctrl+C in attached mode)
#
# Config reload:
#   docker exec <container> kill -HUP 1
#   # Or mount a new config and send SIGHUP

set -e

PRPD="/usr/local/bin/prpd"
PRPD_PID=""
HALT=""

# ------------------- signal handlers -------------------

_cleanup() {
    echo "entrypoint: shutting down..."
    HALT=1

    # Stop prpd first
    if [ -n "$PRPD_PID" ] && kill -0 "$PRPD_PID" 2>/dev/null; then
        echo "entrypoint: stopping prpd (PID ${PRPD_PID})..."
        kill -TERM "$PRPD_PID" 2>/dev/null
    fi

    # Give telnetd a courtesy TERM
    if [ -n "$TELNETD_PID" ]; then
        kill -TERM "$TELNETD_PID" 2>/dev/null || true
    fi

    # Wait for all children to finish
    wait
    exit 0
}

_reload() {
    echo "entrypoint: SIGHUP received — reloading config"

    if [ -n "$PRPD_PID" ] && kill -0 "$PRPD_PID" 2>/dev/null; then
        echo "entrypoint: stopping current prpd (PID ${PRPD_PID})..."
        kill -TERM "$PRPD_PID" 2>/dev/null
        # Wait for the old prpd to fully exit before starting new one
        while kill -0 "$PRPD_PID" 2>/dev/null; do
            sleep 0.1
        done
    fi

    echo "entrypoint: starting new prpd (reads updated config.yaml)..."
    $PRPD &
    PRPD_PID=$!
    echo "entrypoint: prpd started (PID ${PRPD_PID})"
}

# ------------------- start services -------------------

# Start telnetd for GNS3 console access (port 5000).
# -F stays in foreground so the process table is clean.
# Use /usr/sbin/telnetd (busybox-extras) — the base busybox does not
# include telnetd.
/usr/sbin/telnetd -F -l /bin/sh -p 5000 &
TELNETD_PID=$!
echo "entrypoint: telnetd started on port 5000 (PID ${TELNETD_PID})"

# Start the PRP daemon
$PRPD &
PRPD_PID=$!
echo "entrypoint: prpd started (PID ${PRPD_PID})"

# ------------------- signal registration -------------------

trap _cleanup TERM INT
trap _reload HUP

# Main loop: poll every second for child exits.
# This is more portable than wait -n (which needs BusyBox ≥1.36)
# and avoids race conditions with SIGHUP in simpler shells.
while true; do
    # Sleep in the background so traps can interrupt it.
    # When the sleep returns (either after 1s or interrupted
    # by a signal handler), we re-check conditions.
    sleep 1 &
    SLEEP_PID=$!
    wait "$SLEEP_PID" 2>/dev/null || true

    [ -n "$HALT" ] && exit 0

    # If prpd died unexpectedly (e.g. crash), restart it
    if ! kill -0 "$PRPD_PID" 2>/dev/null; then
        echo "entrypoint: prpd (PID ${PRPD_PID}) exited unexpectedly — restarting..."
        $PRPD &
        PRPD_PID=$!
        echo "entrypoint: prpd restarted (PID ${PRPD_PID})"
    fi
done
