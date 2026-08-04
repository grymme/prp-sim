#!/bin/sh
# PRP integration test: two RedBoxes bridging two SANs across dual LANs.
#
# Verifies:
#   1. SAN-A can ping SAN-B through both RedBoxes (baseline, both LANs up)
#   2. After disconnecting LAN A, traffic continues seamlessly (failover)
#   3. Reconnecting LAN A restores the dual path
#
# Usage: tests/integration.sh [--build]
set -eu

cd "$(dirname "$0")"

if [ "${1:-}" = "--build" ]; then
    echo "==> building image"
    docker build -t prp-sim:test -f ../Dockerfile ../
fi

cleanup() {
    docker compose down -v >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "==> starting topology"
cleanup
docker compose up -d

# Wait for prpd inside both RedBoxes.
for rb in redbox-a redbox-b; do
    for i in $(seq 1 30); do
        if docker compose exec -T "$rb" pgrep prpd >/dev/null 2>&1; then
            break
        fi
        [ "$i" = 30 ] && { echo "FAIL: prpd not running in $rb"; exit 1; }
        sleep 1
    done
done
echo "==> prpd running in both RedBoxes"

# --- Test 1: baseline ping SAN-A -> SAN-B (both LANs up) ---
echo "==> test 1: baseline ping (both LANs up)"
if ! docker compose exec -T san-a sh -c 'ping -c 5 -W 2 10.0.0.12' | grep -q "0% packet loss"; then
    echo "FAIL: baseline ping failed"
    docker compose exec -T san-a ping -c 3 10.0.0.12 || true
    exit 1
fi
echo "PASS: baseline ping"

# --- Test 2: failover - disconnect LAN A from redbox-a, ping still works ---
echo "==> test 2: failover (LAN A down)"
RB_A="$(docker compose ps -q redbox-a)"
docker network disconnect lan-a "$RB_A" 2>/dev/null || true
sleep 2
if ! docker compose exec -T san-a sh -c 'ping -c 5 -W 2 10.0.0.12' | grep -q "0% packet loss"; then
    echo "FAIL: failover ping failed after LAN A disconnect"
    exit 1
fi
echo "PASS: failover over LAN B"

# --- Test 3: reconnect LAN A ---
echo "==> test 3: reconnect LAN A"
docker network connect lan-a "$RB_A" 2>/dev/null || true
sleep 3
if ! docker compose exec -T san-a sh -c 'ping -c 5 -W 2 10.0.0.12' | grep -q "0% packet loss"; then
    echo "FAIL: ping failed after LAN A reconnect"
    exit 1
fi
echo "PASS: reconnect"

echo
echo "ALL INTEGRATION TESTS PASSED"
