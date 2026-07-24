# PRP GNS3 Simulation Container — Design Spec

**Date:** 2026-07-24  
**Status:** Approved (pending user review before implementation)  
**Topic:** Userspace PRP (IEC 62439-3) container for GNS3 — Westermo-compatible RedBox / DAN simulation  
**Approach:** Approach 1 — Userspace PRP Engine in Go (userspace daemon `prpd`, TAP interface `prp0`, raw sockets on PRP LAN ports, 3 adapters: eth0/LAN A, eth1/LAN B, eth2/Interlink)

---

## 1. Architecture Overview

The container runs a single Go binary (`prpd`). Key interfaces:
- `eth0` — PRP LAN A
- `eth1` — PRP LAN B
- `eth2` — Interlink (SAN / management / RedBox mode only)
- `prp0` — TAP interface (L2 virtual device) used by DAN-mode applications; appears as a real Ethernet interface

`prpd` creates `prp0` at startup, binds raw sockets to `eth0`/`eth1`, reads the YAML config, and enters a main `select()` loop across interfaces + timers.

```dot
  eth0 (LAN A) ──┐
                 ├──▶ [prpd daemon] ──▶ eth0+eth1 send/recv
  eth1 (LAN B) ──┘     (Go binary)      │
  eth2 (Int.) ───┐                        ▼
                 │              ┌─────────────┐
  prp0 (TAP) ◀───┘              │  Node Table │
  (DAN L2)                      │  Supervision │
                                │  RCT encode  │
                                │  Dup detect   │
                                └─────────────┘
```

### Modes
- `role: redbox` — bridges Interlink (`eth2`) traffic into PRP LAN A/B; duplicates Interlink frames with PRP RCT trailer; strips trailer and discards duplicates on receive, forwarding original frame to Interlink.
- `role: dan` — application traffic through `prp0`; `prpd` inserts RCT trailer and sends duplicates to both `eth0`/`eth1`; strips RCT and removes duplicates on receive, writing original frame to `prp0`.

---

## 2. Configuration Model

Single YAML file `/etc/prp/config.yaml`. Key sections:

```yaml
node:
  name: "prp-node-1"
  role: redbox          # redbox | dan

interfaces:
  lan_a: eth0
  lan_b: eth1
  interlink: eth2       # used in redbox mode for SAN bridge

virtual_iface:
  name: prp0
  mac: "auto"            # derived from eth0, or explicit

prp:
  prp_id: 1             # 1-6 (standard); 0 for HSR-SAN
  lan_id: "A"            # A or B (used in HSR-PRP coupling)
  suffix: "0x8100"       # RCT suffix: 0x8100 or 0xFACE
  trailer_enabled: true

supervision:
  enabled: true
  life_check_interval: 2s
  node_forget_time: 64s
  proxy_node_forget_time: 64s
  node_reboot_interval: 500ms

duplicate_detection:
  entry_forget_time: 640ms
  max_node_table_size: 256

multicast_filter:
  first_octet: "01-00-5E"

interlink:
  mode: san             # san | hsr | prp
  forward_all: true
  vlan_filter: []
```

Environment variable overrides supported: `PRP_CONFIG` (base64 YAML) or `PRP_ROLE`, `PRP_PRP_ID`.

---

## 3. PRP Frame Processing

### 3.1 Transmit Path (RedBox — Interlink → PRP LANs)

1. Read raw Ethernet frame from `eth2` (Interlink).
2. Check VLAN tag presence: PRP RCT trailer inserted **after** VLAN tag per IEC 62439-3.
3. Assign 16-bit sequence number per `(src MAC, LAN)`.
4. Build 4-byte RCT trailer with: SequenceNo, LAN ID, LSDUsize, Suffix.
5. Send modified frame to both `eth0` and `eth1` with same sequence number.

### 3.2 Receive Path (PRP LANs → RedBox — Interlink)

1. Read from `eth0` or `eth1`.
2. Detect RCT trailer via suffix (`0x8100` or `0xFACE`). Verify `LSDUsize`.
3. Extract `(src MAC, SequenceNo)`.
4. Duplicate detection: look up `(src MAC, SequenceNo)` in node table; discard if found. Otherwise insert with `entry_forget_time` (640ms) expiry.
5. Strip RCT trailer.
6. Forward original frame to `eth2` (Interlink).

### 3.3 DAN Mode (Application Traffic)
- Send: app writes to `prp0` → `prpd` inserts RCT, duplicates to both LANs.
- Receive: `prpd` reads both LANs → strips RCT, duplicate detection → writes original to `prp0`.

---

## 4. Supervision & Node Table

### 4.1 Supervision Frame
Sent at `life_check_interval` (default 2s) to both LANs. Ethertype `0x88fb`.
Contents: node identity (MAC + PRP-ID + LAN-ID), node state (`active`), sequence number, number of proxy nodes, proxy node list (MACs + age).

In RedBox mode: supervision also advertises proxy nodes (MACs learned on Interlink) so the PRP network knows SANs are reachable through this node.

### 4.2 Node Table
Per-node entries:
- `src_mac`, `sequence_no`, `lan_id`, `port`, `is_proxy`, `last_seen`, `expiry`
- Duplicate sub-table: key `(src_mac, sequence_no)`, expiry `entry_forget_time`.

Stale nodes removed after `node_forget_time` (64s).

---

## 5. GNS3 Integration & Distribution

### 5.1 Docker Appliance
Public GitHub repo `grymme/prp-sim`. Distribution via:
- Docker image: `ghcr.io/<org>/prp-gns3:latest`
- `.gns3a` appliance file included in repo; importable via GNS3 File → Import appliance.

`.gns3a` defines:
- `docker.adapters: 3`
- Image reference pointing to `ghcr.io`
- `port_name_format: "Port {0}"` mapped to labels `Port A`, `Port B`, `Port C - Interlink`
- Console type `telnet`

### 5.2 Colleague Workflow
```
1. Download .gns3a from GitHub release/repo
2. GNS3 → File → Import appliance
3. Drag node into topology
4. Connect Port A → LAN A, Port B → LAN B, Interlink → SAN
5. Start node — pulls image automatically
```

### 5.3 Configuration Override
Per-node customization via GNS3 template environment variables (`PRP_CONFIG_BASE64`) or GNS3 "Extra volumes" mounting `/etc/prp/config.yaml`.

---

## 6. Error Handling, Stability & Debuggability

### 6.1 Stability
- Config validation against JSON Schema before interface creation; invalid config exits with code 3 (Docker health-check compatible) with exact error message.
- Interface retry loop: retries binding raw sockets with 500ms backoff up to 10 attempts (handles GNS3 async interface creation).
- Graceful shutdown: SIGTERM closes sockets, flushes TAP interface, stops timer — no orphan interfaces.

### 6.2 Debuggability
- `DEBUG_FRAMES=1` environment variable: logs each transmitted/received frame with RCT hex bytes, sequence number, LAN ID, direction.
- Pcap output: `--pcap=/var/log/prp/capture.pcap` for Wireshark analysis (shows separate streams for LAN A / LAN B / Interlink).
- JSON logging: `LOG_FORMAT=json` produces structured logs (`{"event":"supervision","lan":"A","seq":42}`).
- HTTP health/status endpoint (`/status` on port 8080): JSON response with interface states, node table entries, sequence counters, supervision stats.

### 6.3 Monitoring Commands
`prpd --status` CLI prints live state: interface links, node table, sequence counters, supervision stats.

---

## 7. Testing Strategy

- **Unit tests**: RCT encoding/decoding, sequence number wrap behavior, VLAN insertion order, duplicate detection logic.
- **Integration harness** (`tests/docker-compose.yml`): 3 containers (2 RedBoxes + 1 SAN) connected via Docker networks simulating LAN A and LAN B. Validates full PRP traffic flow.
- **CI pipeline** (`.github/workflows/`): build Docker image → validate `.gns3a` → run unit tests → run integration harness → publish image to `ghcr.io` on tag.

---

## 8. Design for Isolation & Clarity

- Each subsystem (`prpd` daemon, `engine/` RCT/duplicate logic, `supervision/`, `nodetable/`, `tap/`) operates through well-defined interfaces.
- `prpd` does not expose internal state to applications; applications interact only through the standard Ethernet interface (`prp0` or `eth2`).
- Changing the PRP engine internals (e.g., switching sequence number allocation) does not affect the external interface contract.

---

## 9. Design Self-Review

- **Placeholder scan**: None (`TBD` / `TODO` removed; all fields defined).
- **Internal consistency**: Frame processing direction (interlink → LAN A/B) matches architecture; supervision description matches WeOS behavior; node table expiry times match standard and config fields.
- **Scope check**: Single focused spec for PRP container simulation. No unrelated networking features.
- **Ambiguity check**: Role (`redbox`/`dan`) defined explicitly. Interface mapping (`eth0`/`eth1`/`eth2`) unambiguous. Trailer suffix (`0x8100`/`0xFACE`) specified.

---

## 10. Next Step (Post-Approval)

Once user approves this spec, invoke the `writing-plans` skill to create an implementation plan.

---

*Commit target: `docs/superpowers/specs/2026-07-24-prp-simulation-container-design.md`*
