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

4. Start the nodes. Open the **IED Subscriber** console: it prints a live
   summary (`unique=` / `dupes=`) — zero duplicates is the PRP proof.

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

Publisher and subscriber must share the same `IEC_APPID`.

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

---

## Verification

- `go test ./...` — unit + edge-case tests
- `./tests/integration.sh` — 12-test Docker e2e suite (builds locally,
  cleans up after itself; no GHCR access needed)
- The IED subscriber console showing `dupes=0` across failover tests
  demonstrates exactly-once delivery.
