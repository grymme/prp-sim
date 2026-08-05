#!/bin/sh
# HSR integration test: HSR ring + HSR-PRP coupling topologies.
#
#   test A: 3-node HSR ring — SAN ping across the ring, then break one
#           ring link and verify seamless failover.
#   test B: HSR-PRP dual-RedBox coupling — ring SANs and PRP LAN SANs
#           interoperate; GOOSE delivered exactly-once across the ring.
#
# Usage: tests/hsr-integration.sh
set -eu

cd "$(dirname "$0")"

echo "==> building image"
docker build -t prp-sim:test -f ../Dockerfile ../

SCRIPT_DIR="$(pwd)"
cleanup() {
    for d in "$SCRIPT_DIR/topologies/hsr-ring" "$SCRIPT_DIR/topologies/hsr-prp-coupling"; do
        (cd "$d" && docker compose down -v >/dev/null 2>&1 || true)
    done
    docker ps -aq --filter 'name=^(tg-|tests-)' | xargs -r docker rm -f >/dev/null 2>&1 || true
    docker image rm -f prp-sim:test >/dev/null 2>&1 || true
}
trap cleanup EXIT

# --- Test A: HSR ring, SAN ping + ring-break failover ---
cd "$SCRIPT_DIR/topologies/hsr-ring"
echo "==> starting HSR ring topology"
docker compose up --pull never -d

for rb in redbox-a redbox-b redbox-c; do
    for i in $(seq 1 30); do
        docker compose exec -T "$rb" pgrep prpd >/dev/null 2>&1 && break
        [ "$i" = 30 ] && { echo "FAIL: prpd not running in $rb"; exit 1; }
        sleep 1
    done
done
echo "==> prpd running in all 3 HSR RedBoxes"

echo "==> test A1: SAN ping across the HSR ring"
if ! docker compose exec -T san-1 sh -c 'ping -c 5 -W 2 10.0.0.12' | grep -q "0% packet loss"; then
    echo "FAIL: HSR ring baseline ping failed"
    exit 1
fi
echo "PASS: HSR ring baseline ping"

echo "==> test A2: ring-break failover (disconnect one ring link)"
RB_A="$(docker compose ps -q redbox-a)"
docker network disconnect ring31 "$RB_A" 2>/dev/null || true
sleep 2
if ! docker compose exec -T san-1 sh -c 'ping -c 5 -W 2 10.0.0.12' | grep -q "0% packet loss"; then
    echo "FAIL: HSR ping failed after ring link disconnect"
    exit 1
fi
echo "PASS: HSR ring-break failover"
cd ../..

# --- Test B: HSR-PRP dual RedBox coupling ---
cd "$SCRIPT_DIR/topologies/hsr-prp-coupling"
echo "==> starting HSR-PRP coupling topology"
docker compose up --pull never -d

for rb in hsr-redbox-d hsr-redbox-e hsr-prp-a hsr-prp-b redbox-a redbox-b; do
    for i in $(seq 1 30); do
        docker compose exec -T "$rb" pgrep prpd >/dev/null 2>&1 && break
        [ "$i" = 30 ] && { echo "FAIL: prpd not running in $rb"; exit 1; }
        sleep 1
    done
done
echo "==> prpd running in all 6 RedBoxes"

echo "==> test B1: SAN ping ring-to-PRP (san-r1 -> san-a)"
if ! docker compose exec -T san-r1 sh -c 'ping -c 5 -W 2 10.30.0.12' | grep -q "0% packet loss"; then
    echo "FAIL: ring-to-PRP ping failed"
    exit 1
fi
echo "PASS: ring-to-PRP ping"

echo "==> test B2: SAN ping PRP-to-ring (san-a -> san-r1)"
if ! docker compose exec -T san-a sh -c 'ping -c 5 -W 2 10.30.0.11' | grep -q "0% packet loss"; then
    echo "FAIL: PRP-to-ring ping failed"
    exit 1
fi
echo "PASS: PRP-to-ring ping"

echo "==> test B3: GOOSE across the coupling, exactly-once"
# Sender injects on the HSR ring; both hsr-prp RedBoxes deliver the frame
# to their coupled PRP LANs (LAN A and LAN B). redbox-a (prp-san) receives
# both LAN copies and must dedup them to exactly-once on san-a's segment.
docker run -d --name tg-recv-hsr --privileged \
    --network hsr-prp-coupling_san-net-a \
    --entrypoint trafficgen prp-sim:test --mode recv --iface eth0 --appid 0x1001 --duration 10s \
    >/dev/null 2>&1 || true
sleep 2
# Publisher sits on the RING SAN segment (behind hsr-redbox-d), so its
# frames cross the HSR ring, are coupled onto LAN A and LAN B by the two
# hsr-prp RedBoxes, and must be deduplicated exactly-once by redbox-a.
docker run --rm --privileged \
    --network hsr-prp-coupling_san-net-r1 \
    --entrypoint trafficgen prp-sim:test --mode send --iface eth0 --appid 0x1001 \
    --count 50 --rate 50 \
    >/dev/null 2>&1 || true
docker wait tg-recv-hsr >/dev/null 2>&1 || true
TG=$(docker logs tg-recv-hsr 2>&1)
echo "$TG" | grep '^recv:'
UNIQ=$(echo "$TG" | grep '^recv:' | sed -n 's/.*unique=\([0-9]*\).*/\1/p' || true)
DUPES=$(echo "$TG" | grep '^recv:' | sed -n 's/.*dupes=\([0-9]*\).*/\1/p' || true)
docker rm -f tg-recv-hsr >/dev/null 2>&1 || true
if [ -z "$DUPES" ] || [ "$DUPES" -gt 0 ]; then
    echo "FAIL: coupling GOOSE duplicates detected (dupes=${DUPES:-none})"
    exit 1
fi
if [ -z "$UNIQ" ] || [ "$UNIQ" -lt 45 ]; then
    echo "FAIL: coupling GOOSE loss too high (unique=${UNIQ:-none} of 50)"
    exit 1
fi
echo "PASS: coupling GOOSE exactly-once (unique=$UNIQ, dupes=$DUPES)"
cd ../..

echo ""
echo "ALL HSR TESTS PASSED"
