#!/bin/sh
# HSR integration test: HSR ring + HSR-PRP coupling topologies.
#
#   test A: 3-node HSR ring —
#     A1 SAN ping across the ring
#     A2 ring-break failover
#     A3 GOOSE burst across the ring, exactly-once
#     A4 no storm / loop-freedom check
#     A5 ring node restart mid-traffic (no stale-dup drops)
#     A6 full-size frames (MTU + HSR tag overhead)
#   test B: HSR-PRP dual-RedBox coupling —
#     B1/B2 SAN ping ring<->PRP both directions
#     B3 GOOSE across the coupling, exactly-once
#     B4 coupling GOOSE during ring-break failover
#     B5 coupling GOOSE during PRP LAN A failure
#     B6 coupling RedBox restart mid-traffic
#     B7 VLAN-tagged GOOSE through the coupling
#     B8 bidirectional concurrent traffic through the coupling
#
# Usage: tests/hsr-integration.sh
set -eu

cd "$(dirname "$0")"

echo "==> building image"
docker build -t prp-sim:test -f ../Dockerfile ../

SCRIPT_DIR="$(pwd)"
cleanup() {
    for d in "$SCRIPT_DIR/topologies/hsr-ring" "$SCRIPT_DIR/topologies/hsr-prp-coupling" "$SCRIPT_DIR/topologies/hsr-hsr-quadbox"; do
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

# Reconnect the ring link for the remaining ring tests.
docker network connect ring31 "$RB_A" 2>/dev/null || true
sleep 3

# --- Test A3: GOOSE with retransmit bursts across the ring, exactly-once ---
echo "==> test A3: GOOSE burst across the HSR ring, exactly-once"
docker run -d --name tg-recv-a3 --privileged \
    --network hsr-ring_san-net-2 \
    --entrypoint trafficgen prp-sim:test --mode recv --iface eth0 --appid 0x1001 --duration 15s \
    >/dev/null 2>&1 || true
sleep 2
# Publisher on san-net-1 (behind redbox-a); retransmit bursts (2/4/8 ms)
# mean 100 events -> 400 frames; all must arrive exactly once.
docker run --rm --privileged \
    --network hsr-ring_san-net-1 \
    --entrypoint trafficgen prp-sim:test --mode send --iface eth0 --appid 0x1001 \
    --count 100 --rate 100 --burst \
    >/dev/null 2>&1 || true
docker wait tg-recv-a3 >/dev/null 2>&1 || true
TG3=$(docker logs tg-recv-a3 2>&1)
echo "$TG3" | grep '^recv:' || true
U3=$(echo "$TG3" | grep '^recv:' | sed -n 's/.*unique=\([0-9]*\).*/\1/p' || true)
D3=$(echo "$TG3" | grep '^recv:' | sed -n 's/.*dupes=\([0-9]*\).*/\1/p' || true)
docker rm -f tg-recv-a3 >/dev/null 2>&1 || true
if [ -z "$D3" ] || [ "$D3" -gt 0 ]; then
    echo "FAIL: ring GOOSE burst duplicates (dupes=${D3:-none})"
    exit 1
fi
if [ -z "$U3" ] || [ "$U3" -lt 380 ]; then
    echo "FAIL: ring GOOSE burst loss (unique=${U3:-none} of ~400)"
    exit 1
fi
echo "PASS: HSR ring GOOSE burst exactly-once (unique=$U3, dupes=$D3)"

# --- Test A4: no storm on the ring (loop-freedom) ---
# Send a burst, stop, then watch the engine counters over a quiet window.
# A true loop would keep incrementing ringA/ringB out with every lap;
# ambient Docker multicast (mDNS/SSDP via the SAN) adds only a couple of
# frames per second, so a small tolerance is applied.
echo "==> test A4: no storm on the HSR ring"
docker run --rm --privileged \
    --network hsr-ring_san-net-1 \
    --entrypoint trafficgen prp-sim:test --mode send --iface eth0 --appid 0x1001 \
    --count 50 --rate 50 \
    >/dev/null 2>&1 || true
sleep 2
RC="$(docker compose ps -q redbox-c)"
S1=$(docker logs --since 3s "$RC" 2>&1 | grep 'status:' | tail -1)
CNT1=$(echo "$S1" | sed -n 's/.*ringA out=\([0-9]*\).*ringB out=\([0-9]*\).*/\1+\2/p')
CNT1=$(echo "$CNT1" | bc 2>/dev/null || echo 0)
sleep 8
S2=$(docker logs --since 6s "$RC" 2>&1 | grep 'status:' | tail -1)
CNT2=$(echo "$S2" | sed -n 's/.*ringA out=\([0-9]*\).*ringB out=\([0-9]*\).*/\1+\2/p')
CNT2=$(echo "$CNT2" | bc 2>/dev/null || echo 0)
# Quiet-window growth must stay tiny (ambient multicast only). A 50-frame
# storm circulating the ring would add hundreds of forwards per second.
GROWTH=$((CNT2 - CNT1))
if [ "$GROWTH" -gt 40 ]; then
    echo "FAIL: ring frames still circulating after traffic stopped (ringA+B out grew ${GROWTH} in ~8s)"
    exit 1
fi
echo "PASS: no storm on HSR ring (ringA+B out grew ${GROWTH} in ~8s quiet window)"

# --- Test A5: ring node restart mid-traffic, no stale-dup drops ---
echo "==> test A5: ring node restart mid-GOOSE"
docker run -d --name tg-recv-a5 --privileged \
    --network hsr-ring_san-net-2 \
    --entrypoint trafficgen prp-sim:test --mode recv --iface eth0 --appid 0x1001 --duration 20s \
    >/dev/null 2>&1 || true
sleep 2
docker run --rm --privileged \
    --network hsr-ring_san-net-1 \
    --entrypoint trafficgen prp-sim:test --mode send --iface eth0 --appid 0x1001 \
    --count 300 --rate 30 \
    >/dev/null 2>&1 &
sleep 5
docker compose restart redbox-b >/dev/null 2>&1 || true
sleep 3
READY=0
for i in $(seq 1 20); do
    if docker compose exec -T redbox-b pgrep prpd >/dev/null 2>&1; then
        READY=1
        break
    fi
    sleep 1
    [ "$i" = 20 ] && break
    sleep 0
    true
done
if [ "$READY" != 1 ]; then
    echo "FAIL: redbox-b did not come back up after restart"
    exit 1
fi
wait || true
docker wait tg-recv-a5 >/dev/null 2>&1 || true
TG5=$(docker logs tg-recv-a5 2>&1)
echo "$TG5" | grep '^recv:' || true
U5=$(echo "$TG5" | grep '^recv:' | sed -n 's/.*unique=\([0-9]*\).*/\1/p' || true)
D5=$(echo "$TG5" | grep '^recv:' | sed -n 's/.*dupes=\([0-9]*\).*/\1/p' || true)
docker rm -f tg-recv-a5 >/dev/null 2>&1 || true
if [ -z "$D5" ] || [ "$D5" -gt 0 ]; then
    echo "FAIL: ring node restart caused dup drops (dupes=${D5:-none})"
    exit 1
fi
if [ -z "$U5" ] || [ "$U5" -lt 250 ]; then
    echo "FAIL: ring node restart caused loss (unique=${U5:-none} of 300)"
    exit 1
fi
echo "PASS: ring node restart, no loss/no dupes (unique=$U5, dupes=$D5)"

# --- Test A6: full-size frames (MTU) through the HSR ring ---
echo "==> test A6: full-size frames through the HSR ring (MTU overhead)"
if ! docker compose exec -T san-1 sh -c 'ping -c 3 -W 2 -s 1400 10.0.0.12' | grep -q "0% packet loss"; then
    echo "FAIL: 1400-byte payload through HSR ring"
    exit 1
fi
if ! docker compose exec -T san-1 sh -c 'ping -c 3 -W 2 -s 1472 10.0.0.12' | grep -q "0% packet loss"; then
    echo "FAIL: 1472-byte payload through HSR ring (HSR tag overhead)"
    exit 1
fi
echo "PASS: full-size frames fit through HSR ring (1400B and 1472B payloads)"
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
echo "$TG" | grep '^recv:' || true
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

# --- Test B4: coupling GOOSE during ring-break failover ---
echo "==> test B4: coupling GOOSE during ring-break failover"
docker run -d --name tg-recv-b4 --privileged \
    --network hsr-prp-coupling_san-net-a \
    --entrypoint trafficgen prp-sim:test --mode recv --iface eth0 --appid 0x1001 --duration 15s \
    >/dev/null 2>&1 || true
sleep 2
docker run --rm --privileged \
    --network hsr-prp-coupling_san-net-r1 \
    --entrypoint trafficgen prp-sim:test --mode send --iface eth0 --appid 0x1001 \
    --count 200 --rate 30 \
    >/dev/null 2>&1 &
sleep 4
# Break the ring link between the two coupling RedBoxes (ringab) by
# taking the ring port down at L2 (no interface renumbering; a docker
# network disconnect would re-map ethN and race later tests).
RPA="$(docker compose ps -q hsr-prp-a)"
docker compose exec -T hsr-prp-a sh -c 'ip link set eth0 down' >/dev/null 2>&1 || true
wait || true
docker wait tg-recv-b4 >/dev/null 2>&1 || true
TG4=$(docker logs tg-recv-b4 2>&1)
echo "$TG4" | grep '^recv:' || true
U4=$(echo "$TG4" | grep '^recv:' | sed -n 's/.*unique=\([0-9]*\).*/\1/p' || true)
D4=$(echo "$TG4" | grep '^recv:' | sed -n 's/.*dupes=\([0-9]*\).*/\1/p' || true)
docker rm -f tg-recv-b4 >/dev/null 2>&1 || true
if [ -z "$D4" ] || [ "$D4" -gt 0 ]; then
    echo "FAIL: coupling GOOSE during ring break had dupes (dupes=${D4:-none})"
    exit 1
fi
if [ -z "$U4" ] || [ "$U4" -lt 170 ]; then
    echo "FAIL: coupling GOOSE during ring break loss (unique=${U4:-none} of 200)"
    exit 1
fi
echo "PASS: coupling GOOSE during ring break (unique=$U4, dupes=$D4)"

# Restore the ring link.
docker compose exec -T hsr-prp-a sh -c 'ip link set eth0 up' >/dev/null 2>&1 || true
sleep 3

# --- Test B5: coupling GOOSE during PRP LAN A failure ---
echo "==> test B5: coupling GOOSE during PRP LAN A failure"
docker run -d --name tg-recv-b5 --privileged \
    --network hsr-prp-coupling_san-net-b \
    --entrypoint trafficgen prp-sim:test --mode recv --iface eth0 --appid 0x1001 --duration 15s \
    >/dev/null 2>&1 || true
sleep 2
docker run --rm --privileged \
    --network hsr-prp-coupling_san-net-r1 \
    --entrypoint trafficgen prp-sim:test --mode send --iface eth0 --appid 0x1001 \
    --count 200 --rate 30 \
    >/dev/null 2>&1 &
sleep 4
# Take the LAN A link down on the coupling RedBox; frames must still
# arrive exactly-once via the LAN B coupling RedBox.
RPA2="$(docker compose ps -q hsr-prp-a)"
docker compose exec -T hsr-prp-a sh -c 'ip link set eth2 down' >/dev/null 2>&1 || true
wait || true
docker wait tg-recv-b5 >/dev/null 2>&1 || true
TG5=$(docker logs tg-recv-b5 2>&1)
echo "$TG5" | grep '^recv:' || true
U5b=$(echo "$TG5" | grep '^recv:' | sed -n 's/.*unique=\([0-9]*\).*/\1/p' || true)
D5b=$(echo "$TG5" | grep '^recv:' | sed -n 's/.*dupes=\([0-9]*\).*/\1/p' || true)
docker rm -f tg-recv-b5 >/dev/null 2>&1 || true
if [ -z "$D5b" ] || [ "$D5b" -gt 0 ]; then
    echo "FAIL: coupling GOOSE with LAN A down had dupes (dupes=${D5b:-none})"
    exit 1
fi
if [ -z "$U5b" ] || [ "$U5b" -lt 170 ]; then
    echo "FAIL: coupling GOOSE with LAN A down loss (unique=${U5b:-none} of 200)"
    exit 1
fi
echo "PASS: coupling GOOSE with LAN A down (unique=$U5b, dupes=$D5b)"

# Restore LAN A.
docker compose exec -T hsr-prp-a sh -c 'ip link set eth2 up' >/dev/null 2>&1 || true
sleep 3

# --- Test B6: coupling RedBox restart mid-traffic ---
echo "==> test B6: coupling RedBox restart mid-GOOSE"
docker run -d --name tg-recv-b6 --privileged \
    --network hsr-prp-coupling_san-net-a \
    --entrypoint trafficgen prp-sim:test --mode recv --iface eth0 --appid 0x1001 --duration 20s \
    >/dev/null 2>&1 || true
sleep 2
docker run --rm --privileged \
    --network hsr-prp-coupling_san-net-r1 \
    --entrypoint trafficgen prp-sim:test --mode send --iface eth0 --appid 0x1001 \
    --count 300 --rate 30 \
    >/dev/null 2>&1 &
sleep 5
docker compose restart hsr-prp-a >/dev/null 2>&1 || true
sleep 3
READY=0
for i in $(seq 1 20); do
    if docker compose exec -T hsr-prp-a pgrep prpd >/dev/null 2>&1; then
        READY=1
        break
    fi
    sleep 1
done
if [ "$READY" != 1 ]; then
    echo "FAIL: hsr-prp-a did not come back up after restart"
    exit 1
fi
wait || true
docker wait tg-recv-b6 >/dev/null 2>&1 || true
TG6=$(docker logs tg-recv-b6 2>&1)
echo "$TG6" | grep '^recv:' || true
U6=$(echo "$TG6" | grep '^recv:' | sed -n 's/.*unique=\([0-9]*\).*/\1/p' || true)
D6=$(echo "$TG6" | grep '^recv:' | sed -n 's/.*dupes=\([0-9]*\).*/\1/p' || true)
docker rm -f tg-recv-b6 >/dev/null 2>&1 || true
if [ -z "$D6" ] || [ "$D6" -gt 0 ]; then
    echo "FAIL: coupling restart caused dup drops (dupes=${D6:-none})"
    exit 1
fi
if [ -z "$U6" ] || [ "$U6" -lt 250 ]; then
    echo "FAIL: coupling restart caused loss (unique=${U6:-none} of 300)"
    exit 1
fi
echo "PASS: coupling restart, no loss/no dupes (unique=$U6, dupes=$D6)"

# --- Test B7: VLAN-tagged GOOSE through the coupling ---
echo "==> test B7: VLAN-tagged GOOSE through the coupling"
docker run -d --name tg-recv-b7 --privileged \
    --network hsr-prp-coupling_san-net-a \
    --entrypoint trafficgen prp-sim:test --mode recv --iface eth0 --appid 0x1001 --duration 12s \
    >/dev/null 2>&1 || true
sleep 2
# Real VLAN (VID 100, PCP 4): tag must survive HSR ring + coupling + RCT.
docker run --rm --privileged \
    --network hsr-prp-coupling_san-net-r1 \
    --entrypoint trafficgen prp-sim:test --mode send --iface eth0 --appid 0x1001 \
    --count 60 --rate 60 --vid 100 --pcp 4 \
    >/dev/null 2>&1 || true
docker wait tg-recv-b7 >/dev/null 2>&1 || true
TG7=$(docker logs tg-recv-b7 2>&1)
echo "$TG7" | grep '^recv:' || true
U7=$(echo "$TG7" | grep '^recv:' | sed -n 's/.*unique=\([0-9]*\).*/\1/p' || true)
D7=$(echo "$TG7" | grep '^recv:' | sed -n 's/.*dupes=\([0-9]*\).*/\1/p' || true)
VID7=$(echo "$TG7" | grep '^recv:' | grep -o 'vid=[0-9]* pcp=[0-9]*' | head -1 || true)
docker rm -f tg-recv-b7 >/dev/null 2>&1 || true
if [ -z "$D7" ] || [ "$D7" -gt 0 ]; then
    echo "FAIL: VLAN GOOSE through coupling dupes (dupes=${D7:-none})"
    exit 1
fi
if [ -z "$U7" ] || [ "$U7" -lt 55 ]; then
    echo "FAIL: VLAN GOOSE through coupling loss (unique=${U7:-none} of 60)"
    exit 1
fi
echo "PASS: VLAN-tagged GOOSE through coupling (unique=$U7, dupes=$D7, tags=$VID7)"

# --- Test B8: bidirectional concurrent traffic through the coupling ---
echo "==> test B8: bidirectional concurrent traffic through the coupling"
docker run -d --name tg-recv-b8a --privileged \
    --network hsr-prp-coupling_san-net-a \
    --entrypoint trafficgen prp-sim:test --mode recv --iface eth0 --appid 0x1001 --duration 12s \
    >/dev/null 2>&1 || true
docker run -d --name tg-recv-b8b --privileged \
    --network hsr-prp-coupling_san-net-r1 \
    --entrypoint trafficgen prp-sim:test --mode recv --iface eth0 --appid 0x2002 --duration 12s \
    >/dev/null 2>&1 || true
sleep 2
# Direction 1: ring -> PRP (appid 0x1001). Direction 2: PRP -> ring (0x2002).
docker run --rm --privileged \
    --network hsr-prp-coupling_san-net-r1 \
    --entrypoint trafficgen prp-sim:test --mode send --iface eth0 --appid 0x1001 \
    --count 60 --rate 60 \
    >/dev/null 2>&1 || true
docker run --rm --privileged \
    --network hsr-prp-coupling_san-net-a \
    --entrypoint trafficgen prp-sim:test --mode send --iface eth0 --appid 0x2002 \
    --count 60 --rate 60 --src-mac 02:00:00:00:00:02 \
    >/dev/null 2>&1 || true
docker wait tg-recv-b8a >/dev/null 2>&1 || true
docker wait tg-recv-b8b >/dev/null 2>&1 || true
TG8a=$(docker logs tg-recv-b8a 2>&1)
TG8b=$(docker logs tg-recv-b8b 2>&1)
echo "ring->PRP:  $(echo "$TG8a" | grep '^recv:' || true)"
echo "PRP->ring:  $(echo "$TG8b" | grep '^recv:' || true)"
U8a=$(echo "$TG8a" | grep '^recv:' | sed -n 's/.*unique=\([0-9]*\).*/\1/p' || true)
D8a=$(echo "$TG8a" | grep '^recv:' | sed -n 's/.*dupes=\([0-9]*\).*/\1/p' || true)
U8b=$(echo "$TG8b" | grep '^recv:' | sed -n 's/.*unique=\([0-9]*\).*/\1/p' || true)
D8b=$(echo "$TG8b" | grep '^recv:' | sed -n 's/.*dupes=\([0-9]*\).*/\1/p' || true)
docker rm -f tg-recv-b8a tg-recv-b8b >/dev/null 2>&1 || true
if { [ -z "$D8a" ] || [ "$D8a" -gt 0 ]; } || { [ -z "$D8b" ] || [ "$D8b" -gt 0 ]; }; then
    echo "FAIL: bidirectional coupling GOOSE dupes (A: ${D8a:-none}, B: ${D8b:-none})"
    exit 1
fi
if { [ -z "$U8a" ] || [ "$U8a" -lt 55 ]; } || { [ -z "$U8b" ] || [ "$U8b" -lt 55 ]; }; then
    echo "FAIL: bidirectional coupling GOOSE loss (A: ${U8a:-none}, B: ${U8b:-none})"
    exit 1
fi
echo "PASS: bidirectional coupling GOOSE exactly-once (A unique=$U8a dupes=$D8a; B unique=$U8b dupes=$D8b)"
cd ../..

# --- Test C: HSR-HSR QuadBox coupling (two rings) ---
cd "$SCRIPT_DIR/topologies/hsr-hsr-quadbox"
echo "==> starting HSR-HSR QuadBox topology"
docker compose up --pull never -d

for rb in hsr-redbox-d hsr-hsr-a hsr-hsr-b hsr-redbox-e; do
    for i in $(seq 1 30); do
        docker compose exec -T "$rb" pgrep prpd >/dev/null 2>&1 && break
        [ "$i" = 30 ] && { echo "FAIL: prpd not running in $rb"; exit 1; }
        sleep 1
    done
done
echo "==> prpd running in all 4 RedBoxes"

echo "==> test C1: SAN ping ring1 -> ring2 across the QuadBox"
if ! docker compose exec -T san-r1 sh -c 'ping -c 5 -W 2 10.40.0.12' | grep -q "0% packet loss"; then
    echo "FAIL: ring1->ring2 ping across QuadBox failed"
    exit 1
fi
echo "PASS: QuadBox ping ring1->ring2"

echo "==> test C2: SAN ping ring2 -> ring1 across the QuadBox"
if ! docker compose exec -T san-r2 sh -c 'ping -c 5 -W 2 10.40.0.11' | grep -q "0% packet loss"; then
    echo "FAIL: ring2->ring1 ping across QuadBox failed"
    exit 1
fi
echo "PASS: QuadBox ping ring2->ring1"

echo "==> test C3: GOOSE across both rings, exactly-once"
docker run -d --name tg-recv-c3 --privileged \
    --network hsr-hsr-quadbox_san-net-r2 \
    --entrypoint trafficgen prp-sim:test --mode recv --iface eth0 --appid 0x1001 --duration 12s \
    >/dev/null 2>&1 || true
sleep 2
docker run --rm --privileged \
    --network hsr-hsr-quadbox_san-net-r1 \
    --entrypoint trafficgen prp-sim:test --mode send --iface eth0 --appid 0x1001 \
    --count 60 --rate 60 \
    >/dev/null 2>&1 || true
docker wait tg-recv-c3 >/dev/null 2>&1 || true
TGC3=$(docker logs tg-recv-c3 2>&1)
echo "$TGC3" | grep '^recv:' || true
UC3=$(echo "$TGC3" | grep '^recv:' | sed -n 's/.*unique=\([0-9]*\).*/\1/p' || true)
DC3=$(echo "$TGC3" | grep '^recv:' | sed -n 's/.*dupes=\([0-9]*\).*/\1/p' || true)
docker rm -f tg-recv-c3 >/dev/null 2>&1 || true
if [ -z "$DC3" ] || [ "$DC3" -gt 0 ]; then
    echo "FAIL: QuadBox GOOSE duplicates (dupes=${DC3:-none})"
    exit 1
fi
if [ -z "$UC3" ] || [ "$UC3" -lt 55 ]; then
    echo "FAIL: QuadBox GOOSE loss (unique=${UC3:-none} of 60)"
    exit 1
fi
echo "PASS: QuadBox GOOSE exactly-once (unique=$UC3, dupes=$DC3)"

echo "==> test C4: ring 2 break, traffic ring1 -> ring2 continues"
# Two ring links per ring (ring2-ab and ring2-ba); disconnect one.
docker run -d --name tg-recv-c4 --privileged \
    --network hsr-hsr-quadbox_san-net-r2 \
    --entrypoint trafficgen prp-sim:test --mode recv --iface eth0 --appid 0x1001 --duration 15s \
    >/dev/null 2>&1 || true
sleep 2
docker run --rm --privileged \
    --network hsr-hsr-quadbox_san-net-r1 \
    --entrypoint trafficgen prp-sim:test --mode send --iface eth0 --appid 0x1001 \
    --count 200 --rate 30 \
    >/dev/null 2>&1 &
sleep 4
RBE="$(docker compose ps -q hsr-redbox-e)"
docker network disconnect hsr-hsr-quadbox_ring2-ab "$RBE" 2>/dev/null || true
wait || true
docker wait tg-recv-c4 >/dev/null 2>&1 || true
TGC4=$(docker logs tg-recv-c4 2>&1)
echo "$TGC4" | grep '^recv:' || true
UC4=$(echo "$TGC4" | grep '^recv:' | sed -n 's/.*unique=\([0-9]*\).*/\1/p' || true)
DC4=$(echo "$TGC4" | grep '^recv:' | sed -n 's/.*dupes=\([0-9]*\).*/\1/p' || true)
docker rm -f tg-recv-c4 >/dev/null 2>&1 || true
if [ -z "$DC4" ] || [ "$DC4" -gt 0 ]; then
    echo "FAIL: QuadBox GOOSE with ring 2 break dupes (dupes=${DC4:-none})"
    exit 1
fi
if [ -z "$UC4" ] || [ "$UC4" -lt 170 ]; then
    echo "FAIL: QuadBox GOOSE with ring 2 break loss (unique=${UC4:-none} of 200)"
    exit 1
fi
echo "PASS: QuadBox GOOSE with ring 2 break (unique=$UC4, dupes=$DC4)"
cd ../..

echo ""
echo "ALL HSR TESTS PASSED"
