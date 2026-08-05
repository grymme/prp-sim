#!/bin/sh
# entrypoint.sh — manages telnetd and the node's main service.
#
# Two personalities, selected by the IEC_MODE environment variable:
#
#   (unset / empty)  — PRP node: telnetd + prpd (RedBox or DAN depending
#                      on PRP_ROLE). This is the original behaviour.
#   IEC_MODE=publisher  — IEC 61850 IED: telnetd + trafficgen publishing
#                      GOOSE/SV frames on eth0 (--loop, so it runs until
#                      the container stops).
#   IEC_MODE=subscriber — IEC 61850 IED: telnetd + trafficgen listening
#                      for the configured appid, printing a live summary
#                      (unique/dupes) to the GNS3 console.
#
# All IEC_* environment variables are optional; defaults are chosen so a
# publisher/subscriber pair works out of the box:
#   IEC_APPID   default 0x1001 (hex)        — GOOSE application ID
#   IEC_RATE    default 5      (Hz)         — publish rate (publisher)
#   IEC_VID     default 0                   — VLAN ID (0 = null VLAN +
#                                              priority tagging, the IEC
#                                              61850 legacy default)
#   IEC_PCP     default 4                   — VLAN priority bits
#   IEC_ET      default goose               — goose | sv
#   IEC_IFACE   default eth0                — NIC to use
#   IEC_MCAST   optional destination MAC (defaults per IEC_ET)
#
# Signals:
#   SIGHUP  — reload: restart the main service (prpd or trafficgen)
#   SIGTERM — graceful shutdown of all child processes
#   SIGINT  — same as SIGTERM (Ctrl+C in attached mode)

set -e

PRPD="/usr/local/bin/prpd"
MAIN_PID=""
HALT=""

# ------------------- main service selection -------------------

_start_main() {
    if [ -n "${IEC_MODE:-}" ] && [ "${IEC_MODE}" != "none" ]; then
        # IEC 61850 IED personality.
        IEC_APPID="${IEC_APPID:-0x1001}"
        IEC_RATE="${IEC_RATE:-5}"
        IEC_VID="${IEC_VID:-0}"
        IEC_PCP="${IEC_PCP:-4}"
        IEC_ET="${IEC_ET:-goose}"
        IEC_IFACE="${IEC_IFACE:-eth0}"
        # IEC_MODE is publisher/subscriber; trafficgen expects send/recv.
        case "${IEC_MODE}" in
            publisher)  TG_MODE="send" ;;
            subscriber) TG_MODE="recv" ;;
            *) echo "entrypoint: unknown IEC_MODE ${IEC_MODE} (want publisher|subscriber)"; exit 1 ;;
        esac
        ARGS="--mode ${TG_MODE} --iface ${IEC_IFACE} --appid ${IEC_APPID} --et ${IEC_ET} --loop"
        if [ "${IEC_MODE}" = "publisher" ]; then
            ARGS="${ARGS} --rate ${IEC_RATE} --vid ${IEC_VID} --pcp ${IEC_PCP} --burst"
            [ -n "${IEC_MCAST:-}" ] && ARGS="${ARGS} --mcast ${IEC_MCAST}"
        fi
        echo "entrypoint: IEC mode ${IEC_MODE} (appid ${IEC_APPID}) starting trafficgen..."
        trafficgen ${ARGS} &
    else
        echo "entrypoint: PRP mode starting prpd..."
        $PRPD &
    fi
    MAIN_PID=$!
    echo "entrypoint: main service started (PID ${MAIN_PID})"
}

# ------------------- signal handlers -------------------

_cleanup() {
    echo "entrypoint: shutting down..."
    HALT=1

    # Stop the main service first
    if [ -n "$MAIN_PID" ] && kill -0 "$MAIN_PID" 2>/dev/null; then
        echo "entrypoint: stopping main service (PID ${MAIN_PID})..."
        kill -TERM "$MAIN_PID" 2>/dev/null
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

    if [ -n "$MAIN_PID" ] && kill -0 "$MAIN_PID" 2>/dev/null; then
        echo "entrypoint: stopping current main service (PID ${MAIN_PID})..."
        kill -TERM "$MAIN_PID" 2>/dev/null
        # Wait for the old process to fully exit before starting new one
        while kill -0 "$MAIN_PID" 2>/dev/null; do
            sleep 0.1
        done
    fi

    _start_main
}

# ------------------- start services -------------------

# Start telnetd for GNS3 console access (port 5000).
# -F stays in foreground so the process table is clean.
# Use /usr/sbin/telnetd (busybox-extras) — the base busybox does not
# include telnetd.
/usr/sbin/telnetd -F -l /bin/sh -p 5000 &
TELNETD_PID=$!
echo "entrypoint: telnetd started on port 5000 (PID ${TELNETD_PID})"

# Start the main service (prpd or trafficgen).
_start_main

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

    # If the main service died unexpectedly (e.g. crash), restart it
    if ! kill -0 "$MAIN_PID" 2>/dev/null; then
        echo "entrypoint: main service (PID ${MAIN_PID}) exited unexpectedly — restarting..."
        _start_main
    fi
done
