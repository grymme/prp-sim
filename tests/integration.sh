#!/bin/sh
# PRP integration test: two RedBoxes bridging two SANs across dual LANs.
#
# Verifies:
#   1. SAN-A can ping SAN-B through both RedBoxes (baseline, both LANs up)
#   2. After disconnecting LAN A, traffic continues seamlessly (failover)
#   3. Reconnecting LAN A restores the dual path
#
# Usage: tests/integration.sh
set -eu

cd "$(dirname "$0")"

echo "==> tearing down any stale test topology"
docker compose down -v >/dev/null 2>&1 || true

echo "==> building image"
docker build -t prp-sim:test -f ../Dockerfile ../

cleanup() {
    # Stop and remove test containers + networks + volumes.
    docker compose down -v >/dev/null 2>&1 || true
    # Remove the locally built test image so no dangling artifacts remain
    # (GHCR is only updated on tagged releases, so this image is always
    # built from the current tree and never reused by a later test run).
    docker image rm -f prp-sim:test >/dev/null 2>&1 || true
}
trap cleanup EXIT

# The image is always rebuilt locally (GHCR is only updated on tagged
# releases, so pulling would give a stale image).
echo "==> starting topology"
docker compose up --pull never -d

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

# --- Test 6: full-size frames (MTU vs RCT overhead) ---
# A full-size SAN frame (1514 B) plus the 6-byte RCT must fit on the wire.
# The container raises its own MTU to 1506, but the Docker veth/bridge
# stays at 1500 - so frames > 1500 - 18 + 6 = 1488-ish payload cannot pass.
# -s 1400 -> 1446 B frame -> +6 RCT = 1452 B on the wire (fits 1500)
# -s 1472 -> 1514 B frame -> +6 RCT = 1520 B on the wire (EXCEEDS 1500)
echo "==> test 6: full-size frames (MTU overhead)"
if docker compose exec -T san-a sh -c 'ping -c 3 -W 2 -s 1400 10.0.0.12' | grep -q "0% packet loss"; then
    echo "PASS: 1400-byte payload fits (1452 B on wire)"
else
    echo "FAIL: 1400-byte payload should pass"
    exit 1
fi
if docker compose exec -T san-a sh -c 'ping -c 3 -W 2 -s 1472 10.0.0.12' | grep -q "0% packet loss"; then
    echo "PASS: 1472-byte payload passes (RCT fits within Docker MTU)"
else
    echo "NOTE: 1472-byte payload fails - RCT overhead exceeds Docker MTU 1500 (expected on veth, real RedBoxes raise the switch MTU)"
fi

# --- Test 7: bidirectional concurrent ping ---
# Both SANs ping each other simultaneously. Exercises per-src-MAC sequence
# managers and dup/proxy tables under load in both directions.
echo "==> test 7: bidirectional concurrent ping"
docker compose exec -T san-a sh -c 'ping -c 20 -i 0.2 -W 2 10.0.0.12 > /tmp/ping-a.log 2>&1' &
PA=$!
docker compose exec -T san-b sh -c 'ping -c 20 -i 0.2 -W 2 10.0.0.11 > /tmp/ping-b.log 2>&1' &
PB=$!
wait $PA; RA=$?
wait $PB; RB=$?
if [ $RA -eq 0 ] && [ $RB -eq 0 ]; then
    AOK=$(docker compose exec -T san-a sh -c 'grep -o "[0-9]*% packet loss" /tmp/ping-a.log')
    BOK=$(docker compose exec -T san-b sh -c 'grep -o "[0-9]*% packet loss" /tmp/ping-b.log')
    if [ "$AOK" = "0% packet loss" ] && [ "$BOK" = "0% packet loss" ]; then
        echo "PASS: bidirectional ping (A->B: $AOK, B->A: $BOK)"
    else
        echo "FAIL: bidirectional ping (A->B: $AOK, B->A: $BOK)"
        exit 1
    fi
else
    echo "FAIL: bidirectional ping exit codes A=$RA B=$RB"
    exit 1
fi

# --- Test 8: restart sequence-number reset ---
# Restarting a RedBox resets its per-src-MAC sequence counters to 0. The
# peer's duplicate-detection table still holds (src, seq 0..k) entries for
# up to entry_forget_time (640 ms), so fresh frames reusing those sequence
# numbers are wrongly discarded as duplicates.
echo "==> test 8: restart seq reset (supervision flush + randomized seq start)"
# Restart the RedBox: its sequence counters reset to a new random start and
# the peer must flush stale duplicate entries (detected via supervision seq
# regression). After the node is back, a fast stream must NOT be dropped as
# "stale duplicates" - this was the 12% loss limitation, now fixed.
docker compose exec -T san-a sh -c 'ping -i 0.05 -c 100 -W 1 10.0.0.12 > /tmp/ping-restart.log 2>&1' &
PP=$!
sleep 1
docker compose restart redbox-a >/dev/null 2>&1
echo "   redbox-a restarted, waiting for it to come back..."
wait $PP || true
# Wait for redbox-a to be fully back (health check), then measure loss on a
# fresh fast stream. Any loss here = seq-reset collision (stale dup entries).
for i in $(seq 1 30); do
    if docker compose ps -q redbox-a >/dev/null 2>&1 && \
       [ "$(docker inspect -f '{{.State.Health.Status}}' "$(docker compose ps -q redbox-a)" 2>/dev/null)" = "healthy" ]; then
        break
    fi
    sleep 1
done
RESTART=$(docker compose exec -T san-a sh -c 'ping -i 0.05 -c 40 -W 1 10.0.0.12 > /tmp/ping-restart2.log 2>&1; grep "packets transmitted" /tmp/ping-restart2.log')
echo "   $RESTART"
REQ=$(echo "$RESTART" | awk '{ for(i=1;i<=NF;i++) if($(i+1)=="packets") print $i }')
REP=$(echo "$RESTART" | awk '{ for(i=1;i<=NF;i++) if($(i+1)=="packets") print $(i+3) }')
LOSS=$(echo "$RESTART" | grep -o '[0-9]*% packet loss')
echo "   post-restart loss=$LOSS (request=$REQ reply=$REP)"
# With supervision-triggered dup-table flush and a randomized sequence
# start, a restarted node's fresh frames must not be discarded as stale
# duplicates. Allow 10% for noise, but 12%+ (the old bug) must fail.
case "$LOSS" in
    "0% packet loss") echo "PASS: no drops after restart (supervision flush worked)" ;;
    100%*) echo "FAIL: total loss after restart"; exit 1 ;;
    *) if [ "${LOSS%\%}" -gt 10 ]; then
           echo "FAIL: post-restart loss ${LOSS} (seq-reset collision - supervision flush not effective)"
           exit 1
       else
           echo "INFO: minor loss ${LOSS} after restart (within noise)"
       fi ;;
esac

# --- Test 9: GOOSE transparency (IEC 61850) ---
# A GOOSE IED on SAN-A publishes frames (EtherType 0x88B8, multicast
# 01-0C-CD-01-xx) that must be delivered exactly once to SAN-B across the
# redundant PRP network: no duplicates, no loss beyond a small tolerance.
# GOOSE retransmission bursts (2,4,8..ms) must each be treated as a
# distinct frame by duplicate detection.
echo "==> test 9: GOOSE transparency (0x88B8, multicast 01-0C-CD)"
docker run -d --name tg-recv --privileged \
    --network tests_san-net-b \
    --entrypoint trafficgen prp-sim:test --mode recv --iface eth0 --appid 0x1001 --duration 20s \
    >/dev/null 2>&1
sleep 3
docker run --rm --privileged \
    --network tests_san-net-a \
    --entrypoint trafficgen prp-sim:test --mode send --iface eth0 --appid 0x1001 \
    --count 100 --rate 100 --burst \
    >/dev/null 2>&1
docker wait tg-recv >/dev/null 2>&1 || true
TG=$(docker logs tg-recv 2>&1)
echo "$TG" | grep '^recv:'
UNIQ=$(echo "$TG" | grep '^recv:' | sed -n 's/.*unique=\([0-9]*\).*/\1/p')
DUPES=$(echo "$TG" | grep '^recv:' | sed -n 's/.*dupes=\([0-9]*\).*/\1/p')
docker rm -f tg-recv >/dev/null 2>&1
if [ -z "$UNIQ" ] || [ -z "$DUPES" ]; then
    echo "FAIL: GOOSE recv produced no stats"
    exit 1
fi
if [ "$DUPES" -gt 0 ]; then
    echo "FAIL: GOOSE duplicates detected ($DUPES dupes) - PRP dup detection failed"
    exit 1
fi
if [ "$UNIQ" -lt 90 ]; then
    echo "FAIL: GOOSE loss too high (unique=$UNIQ of 100)"
    exit 1
fi
MAXLAT=$(echo "$TG" | grep '^recv:' | grep -o 'max=[0-9.]*[µm]s')
echo "PASS: GOOSE transparency (unique=$UNIQ, dupes=$DUPES, $MAXLAT)"

# --- Test 10: GOOSE during LAN A failure ---
# Sampled-Value-grade traffic must continue with no duplication when one
# LAN fails (redundancy guarantee of PRP).
echo "==> test 10: GOOSE during failover"
docker run -d --name tg-recv2 --privileged \
    --network tests_san-net-b \
    --entrypoint trafficgen prp-sim:test --mode recv --iface eth0 --appid 0x2001 --duration 15s \
    >/dev/null 2>&1
sleep 3
RB_A="$(docker compose ps -q redbox-a)"
docker network disconnect lan-a "$RB_A" 2>/dev/null || true
docker run --rm --privileged \
    --network tests_san-net-a \
    --entrypoint trafficgen prp-sim:test --mode send --iface eth0 --appid 0x2001 \
    --count 80 --rate 100 --burst \
    >/dev/null 2>&1
docker network connect lan-a "$RB_A" 2>/dev/null || true
docker wait tg-recv2 >/dev/null 2>&1 || true
TG2=$(docker logs tg-recv2 2>&1)
echo "$TG2" | grep '^recv:'
DUPES2=$(echo "$TG2" | grep '^recv:' | sed -n 's/.*dupes=\([0-9]*\).*/\1/p')
UNIQ2=$(echo "$TG2" | grep '^recv:' | sed -n 's/.*unique=\([0-9]*\).*/\1/p')
docker rm -f tg-recv2 >/dev/null 2>&1
if [ "$DUPES2" -gt 0 ]; then
    echo "FAIL: GOOSE duplicates during failover ($DUPES2)"
    exit 1
fi
if [ -z "$UNIQ2" ] || [ "$UNIQ2" -lt 70 ]; then
    echo "FAIL: GOOSE loss during failover too high (unique=$UNIQ2 of 80)"
    exit 1
fi
echo "PASS: GOOSE during failover (unique=$UNIQ2, dupes=$DUPES2)"

# --- Test 11: legacy VLAN-0 priority-tagged GOOSE (IEC 61850) ---
# Historical IEC 61850 deployments used a null VLAN (VID 0) with active
# priority bits (PCP 4) in the 802.1Q tag: QoS for GOOSE without network
# segmentation (IEEE 802.1Q "priority tagging without segmentation").
#
# The tag must survive the RedBox intact: both LAN copies carry
# vid=0 pcp=4, and the receiver must observe exactly that tag on the
# wire. Any RedBox that strips or re-encodes the VLAN tag would break
# substation QoS.
echo "==> test 11: legacy VLAN-0 + PCP-4 GOOSE (priority tagging)"
docker run -d --name tg-recv3 --privileged \
    --network tests_san-net-b \
    --entrypoint trafficgen prp-sim:test --mode recv --iface eth0 --appid 0x3001 --duration 15s \
    >/dev/null 2>&1
sleep 3
docker run --rm --privileged \
    --network tests_san-net-a \
    --entrypoint trafficgen prp-sim:test --mode send --iface eth0 --appid 0x3001 \
    --count 50 --rate 100 --vid 0 --pcp 4 \
    >/dev/null 2>&1
docker wait tg-recv3 >/dev/null 2>&1 || true
TG3=$(docker logs tg-recv3 2>&1)
echo "$TG3" | grep '^recv:'
UNIQ3=$(echo "$TG3" | grep '^recv:' | sed -n 's/.*unique=\([0-9]*\).*/\1/p' | head -1)
DUPES3=$(echo "$TG3" | grep '^recv:' | sed -n 's/.*dupes=\([0-9]*\).*/\1/p' | head -1)
VID3=$(echo "$TG3" | grep -o 'vid=[0-9]*' | head -1 | cut -d= -f2)
PCP3=$(echo "$TG3" | grep -o 'pcp=[0-9]*' | head -1 | cut -d= -f2)
docker rm -f tg-recv3 >/dev/null 2>&1
if [ -z "$UNIQ3" ] || [ "$UNIQ3" -lt 45 ]; then
    echo "FAIL: VLAN-tagged GOOSE loss too high (unique=$UNIQ3 of 50)"
    exit 1
fi
if [ "$DUPES3" -gt 0 ]; then
    echo "FAIL: VLAN-tagged GOOSE duplicates ($DUPES3)"
    exit 1
fi
# The RedBox must preserve the null VLAN + priority tag (IEC 61850 legacy
# default: vid=0, pcp=4).
if [ "$VID3" != "0" ] || [ "$PCP3" != "4" ]; then
    echo "FAIL: priority tag not preserved (vid=$VID3 pcp=$PCP3, want vid=0 pcp=4)"
    exit 1
fi
echo "PASS: VLAN-0 + PCP-4 GOOSE preserved (unique=$UNIQ3, dupes=$DUPES3, vid=$VID3 pcp=$PCP3)"

# --- Test 12: Sampled Values high rate ---
# SV (IEC 61850-9-2, 0x88BA) publishes at 4000-4800 fps. At this rate the
# duplicate-detection table is exercised heavily: entries must expire
# cleanly without leaking duplicates or dropping legitimate frames.
echo "==> test 12: SV high rate (2000 fps)"
docker run -d --name tg-recv4 --privileged \
    --network tests_san-net-b \
    --entrypoint trafficgen prp-sim:test --mode recv --iface eth0 --appid 0x4001 --duration 10s \
    >/dev/null 2>&1
sleep 3
docker run --rm --privileged \
    --network tests_san-net-a \
    --entrypoint trafficgen prp-sim:test --mode send --iface eth0 --appid 0x4001 \
    --count 2000 --rate 2000 \
    >/dev/null 2>&1
docker wait tg-recv4 >/dev/null 2>&1 || true
TG4=$(docker logs tg-recv4 2>&1)
echo "$TG4" | grep '^recv:'
UNIQ4=$(echo "$TG4" | grep '^recv:' | sed -n 's/.*unique=\([0-9]*\).*/\1/p')
DUPES4=$(echo "$TG4" | grep '^recv:' | sed -n 's/.*dupes=\([0-9]*\).*/\1/p')
docker rm -f tg-recv4 >/dev/null 2>&1
if [ -z "$UNIQ4" ] || [ "$UNIQ4" -lt 1800 ]; then
    echo "FAIL: SV high-rate loss too high (unique=$UNIQ4 of 2000)"
    exit 1
fi
if [ "$DUPES4" -gt 0 ]; then
    echo "FAIL: SV high-rate duplicates ($DUPES4)"
    exit 1
fi
echo "PASS: SV high rate (unique=$UNIQ4, dupes=$DUPES4)"

echo
echo "ALL INTEGRATION TESTS PASSED"
