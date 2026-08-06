# PRP Simulator + IEC 61850 IED package

Self-contained distribution of the PRP (IEC 62439-3) simulator for GNS3,
including simulated IEC 61850 GOOSE/SV IED nodes and a standalone Windows
traffic generator for testing against real substation gear.

**No internet required after install** — the Docker image is bundled.

---

## Contents

| Item | Purpose |
|---|---|
| `prp-sim-image.tar.gz` | Docker image (load with `./install.sh`) |
| `gns3/*.gns3a` | GNS3 appliances: RedBox, IED publisher, IED subscriber |
| `gns3/symbols/*.svg` | Node icons |
| `trafficgen-windows-amd64.exe` | Standalone GOOSE/SV generator for Windows |
| `windows/npcap-installer.exe` | Npcap driver installer (Windows, run as admin) |
| `install.sh` | Linux: `docker load` + appliance import instructions |

---

## Linux / GNS3 quick start

1. `chmod +x install.sh && ./install.sh`
2. In GNS3: **File → Import appliance** for each `.gns3a` in `gns3/`
   (restart GNS3 if the templates do not appear).
3. Build a topology:

```
[IED Publisher] ── SAN-A ── [RedBox A] ── LAN-A ── [RedBox B] ── SAN-B ── [IED Subscriber]
                              └──────── LAN-B ───────────┘
```

4. Start the nodes. Open the **IED Subscriber** console: it shows a live
   split-screen stats view — PRP-level counters (frames, duplicates, RCT
   errors, VLAN tags, supervision) on top, GOOSE/SV state (stNum, sqNum,
   allData, latency) below. `unique=` / `dupes=` — zero duplicates is the
   PRP proof. Open the **aux console** (port 5000) for a shell.

### IEC 61850 IED node settings

Both IED appliances are configurable via per-node environment variables
(right-click node → Configure → General → Environment):

| Variable | Default | Meaning |
|---|---|---|
| `IEC_MODE` | (set by appliance) | `publisher` or `subscriber` |
| `IEC_APPID` | `0x1001` | GOOSE application ID (hex) |
| `IEC_ET` | `goose` | `goose` (0x88B8) or `sv` (0x88BA) |
| `IEC_RATE` | `5` | publish rate in Hz (publisher) |
| `IEC_VID` | `0` | VLAN ID; 0 = null VLAN + priority tagging (IEC 61850 legacy default) |
| `IEC_PCP` | `4` | 802.1Q priority bits |
| `IEC_MCAST` | per `IEC_ET` | destination MAC override |
| `IEC_GCB` | `LLN0$GO$gcb0` | GOOSE control-block reference (`gocbRef`) |
| `IEC_DSET` | `LLN0$dataset` | GOOSE dataset reference (`datSet`) |
| `IEC_GOID` | `LLN0$GO$gcb0` | GOOSE ID (`goID`) |
| `IEC_CONFREV` | `1` | configuration revision (`confRev`) |
| `IEC_SVID` | `MU01` | SV `svID` (subscriber filters on this) |
| `IEC_STEVENTS` | `10` | GOOSE state changes per N events (stNum++) |
| `IEC_GCB` | `LLN0$GO$gcb0` | GOOSE control-block reference (`gocbRef`) |
| `IEC_DSET` | `LLN0$dataset` | GOOSE dataset reference (`datSet`) |
| `IEC_GOID` | `LLN0$GO$gcb0` | GOOSE ID (`goID`) |
| `IEC_CONFREV` | `1` | configuration revision (`confRev`) |
| `IEC_SVID` | `MU01` | SV `svID` (subscriber filters on this) |
| `IEC_STEVENTS` | `10` | GOOSE state changes per N events (stNum++) |

Publisher and subscriber must share the same `IEC_APPID` (and, for SV,
`IEC_SVID`).

---

## HSR and HSR-PRP coupling

The same RedBox also runs HSR (IEC 62439-3 Clause 5). Set the role per
node via `PRP_ROLE` (or `node.role` in the config):

- **`hsr-san`** — SAN into an HSR ring. eth0/eth1 are ring ports A/B,
  eth2 is the SAN. Ring frames carry the real 0x892F HSR tag; ring
  failover is seamless.
- **`hsr-prp`** — connect an HSR ring to one PRP LAN (the *dual RedBox*
  coupling: place two of them on the ring, one on LAN A, one on LAN B).
  Configure `HSR_PRP_ID` (NetId, same on both) and `HSR_LAN_ID` (A|B).
  Frames cross the ring↔PRP boundary with the sequence number preserved,
  and the PathId reinjection check (IEC 62439-3 COR1 5.2.2.3.1) keeps
  LAN-A traffic off LAN B.
- **`hsr-hsr`** — connect two HSR rings (QuadBox). Place two `hsr-hsr`
  nodes joined by their interlink ports; each carries its own ring on
  eth0/eth1 and the interlink on eth2. Frames cross between the rings
  with the HSR tag and sequence preserved, forming one redundancy
  domain.

Example ring topology:

```
[SAN] ── [RB hsr-san] ── ring ── [RB hsr-prp, LAN A] ── PRP LAN A
                              │                        └── PRP LAN B
                              └── [RB hsr-prp, LAN B] ──┘
```

Committed Docker topologies: `tests/topologies/hsr-ring/` and
`tests/topologies/hsr-prp-coupling/` (run `tests/hsr-integration.sh`).

---
---

## Hybrid: virtual RedBox + real switches + real RedBox

The simulator speaks real PRP on the wire (RCT trailer, supervision
frames), so a virtual RedBox can pair with real RedBox hardware:

```
GNS3 (on host machine):
[IED Publisher] ─ SAN-A ─ [RedBox A (sim)]
                             │ eth0 ── [Cloud node LAN-A] ── host NIC 1
                             │ eth1 ── [Cloud node LAN-B] ── host NIC 2

Real world:
  host NIC 1 ── real switch A ── RedBox B LAN-A port
  host NIC 2 ── real switch B ── RedBox B LAN-B port
  RedBox B SAN ── real device / Windows laptop running trafficgen
```

### Requirements (critical)

- **GNS3 server runs on the host** (not the GNS3 VM) so Cloud nodes can
  reach the physical NICs.
- **Two real switches, one per LAN** — LAN-A and LAN-B must never be
  bridged together (that would create the loop PRP is designed around).
- **MTU ≥ 1520** (ideally 1524 for VLAN-tagged GOOSE/SV) on: host NICs,
  both switch ports, and the real RedBox PRP ports. A switch port capped
  at 1500 drops full-size PRP frames (1514 B + 6 B RCT = 1520 B).
  Raise the MTU before starting: `sudo ip link set dev <nic> mtu 1524`
- Cloud node binding: right-click the Cloud node → Configure → add the
  correct host interface for that LAN. Each Cloud node must map to its own
  dedicated NIC.
- The real RedBox and the simulator must agree on the PRP network ID and
  both enable supervision (so restart detection and node liveness work).

---

## Windows standalone traffic generator

Useful when the subscriber is a **real device**: run the generator on a
Windows laptop wired into the same test network to receive (or inject)
GOOSE/SV frames from the physical NIC.

### One-time setup

1. Run `windows/npcap-installer.exe` as administrator (default options).
   For non-admin use, check **"Allow non-administrative users to capture
   and send packets"**.
2. Find your NIC's Npcap name:
   ```
   trafficgen-windows-amd64.exe --list-devices
   ```

### Usage

```bat
:: Receive GOOSE appid 0x1001 on the NIC whose description contains "Ethernet"
trafficgen-windows-amd64.exe --mode recv --iface "Ethernet" --appid 0x1001 --loop

:: Publish GOOSE (VLAN 0, PCP 4 — IEC 61850 legacy priority tagging), 5 Hz
trafficgen-windows-amd64.exe --mode send --iface "Ethernet" --appid 0x1001 --rate 5 --vid 0 --pcp 4 --burst --loop

:: Sampled Values stream
trafficgen-windows-amd64.exe --mode send --iface 1 --et sv --appid 0x4001 --rate 2000 --loop
```

`--iface` accepts the Npcap name (`\Device\NPF_{GUID}`), a 1-based index
from `--list-devices`, or a substring of the friendly NIC description.

Run from an elevated prompt if packet send fails with an access error.

### Real IEC 61850 on the wire

The generator emits **real** GOOSE (IEC 61850-8-1) and Sampled Values
(IEC 61850-9-2) APDUs, so Wireshark decodes the full protocol tree
(GOOSE: gocbRef, timeAllowedToLive, stNum/sqNum, allData; SV: svID,
smpCnt, smpRate, sampl) — and any real IED can subscribe.

- GOOSE `stNum` increments on each state change; `sqNum` per retransmission.
- SV `smpCnt` increments per frame (mod 4096).
- `--burst` sends GOOSE-style retransmission bursts (2, 4, 8, 16 … ms)
  like a real IED; each retransmission carries a new `sqNum`.

In the GNS3 console both nodes show a live split-screen TUI
(PRP-level + GOOSE/SV-level stats); the aux console (telnet port 5000)
still gives you a shell.

---

## Verification

- `go test ./...` — unit + edge-case tests
- `./tests/integration.sh` — 12-test Docker e2e suite (builds locally,
  cleans up after itself; no GHCR access needed)
- The IED subscriber console showing `dupes=0` across failover tests
  demonstrates exactly-once delivery.
