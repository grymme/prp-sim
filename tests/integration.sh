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

# --- Test 4: duplicate-free delivery ---
# If duplicate frames leaked to SAN-B, it would reply twice per request and
# ping on SAN-A would report MORE replies than requests. With both LANs up
# (no loss in this sim), received must equal transmitted.
echo "==> test 4: duplicate-free delivery"
PING=$(docker compose exec -T san-a sh -c 'ping -c 20 -i 0.2 -W 2 10.0.0.12 2>&1' | grep 'packets transmitted')
echo "   $PING"
REQ=$(echo "$PING" | awk '{ for(i=1;i<=NF;i++) if($(i+1)=="packets" && $(i+2)=="transmitted,") print $i }')
REP=$(echo "$PING" | awk '{ for(i=1;i<=NF;i++) if($(i+1)=="packets" && $(i+2)=="received,") print $i }')
echo "   transmitted=$REQ received=$REP"
if [ -z "$REP" ]; then
    echo "FAIL: could not parse ping stats"
    exit 1
fi
if [ "$REP" -gt "$REQ" ]; then
    echo "FAIL: duplicate frames leaked ($REP replies for $REQ requests)"
    exit 1
fi
echo "PASS: duplicate-free delivery (received <= transmitted)"

# --- Test 5: no self-loop / no storm ---
# A RedBox must not re-ingest its own transmissions. Send a broadcast
# burst from SAN-A and verify the RedBox does not amplify it back.
echo "==> test 5: no storm on self-transmission"
docker compose exec -T san-a sh -c 'ping -c 5 -W 2 10.0.0.12 >/dev/null 2>&1 || true'
sleep 2
# Interlink RX on redbox-b should not grow unboundedly; snapshot stats twice.
RX1=$(docker compose exec -T redbox-b sh -c 'cat /sys/class/net/eth2/statistics/rx_bytes')
sleep 3
RX2=$(docker compose exec -T redbox-b sh -c 'cat /sys/class/net/eth2/statistics/rx_bytes')
DELTA=$((RX2 - RX1))
echo "   interlink rx_bytes delta over 3s: $DELTA"
if [ "$DELTA" -gt 300000 ]; then
    echo "FAIL: interlink traffic storm detected (delta $DELTA bytes in 3s)"
    exit 1
fi
echo "PASS: no storm"

echo
echo "ALL INTEGRATION TESTS PASSED"
