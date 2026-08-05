# Design — HSR RedBox + HSR-PRP Coupling Support, 2026-08-05

References: Westermo WeOS General + HowTo HSR/PRP guides (docs.westermo.com/weos/weos-5), Linux kernel `net/hsr`, IEC 62439-3.

## Goal
Add HSR (High-availability Seamless Redundancy) to the simulator RedBox. Four WeOS-compatible modes:
- `prp-san` (existing behavior; `redbox` kept as alias) — SAN into two PRP LANs.
- `hsr-san` — SAN into an HSR ring.
- `hsr-prp` — HSR ring into ONE PRP LAN (interlink = PRP LAN A or B); two such RedBoxes form the HSR↔PRP Dual RedBox topology.
- `hsr-hsr` (QuadBox) — deferred to a follow-up (approved); design keeps the engine extensible for it.

## 1 — Architecture & module changes
- **`internal/engine`**: HSR tag codec. Tag = 6 bytes, placed after EtherType AND after any VLAN tag: 4-bit **PathId** + 12-bit **LSDUsize** + 16-bit **seq**, EtherType `0x892F`. `EncodeHSR`, `DecodeHSR`, `IsHSRFrame`. Reuses VLAN-aware `GetEtherType`. LSDU size = payload length excluding DMAC/SMAC/VLAN tags (kernel definition).
- **`internal/supervision`**: extend existing builder/parser — same `0x88FB` frame, `path` field already in layout (0 for PRP; NetId|LanId for HSR). Ring supervision is HSR-tagged without RCT; PRP LAN supervision keeps RCT. REDBOX_MAC TLV already present for proxy announcements.
- **`internal/prp`**: Node dispatches on role (`prp-san`, `hsr-san`, `hsr-prp`). Ring ports reuse A/B raw-socket machinery; interlink semantics vary per role. nodetable, seqMgr, proxy table, multicast filter shared unchanged.

## 2 — Config schema
- `node.role`: `prp-san` | `hsr-san` | `hsr-prp` (`redbox` alias for `prp-san`).
- `hsr-prp` only: `interlink.lan_id: A|B`, `interlink.net_id: 1..6` (NetId shared across the coupling).
- Env overrides: `PRP_ROLE`, `PRP_LAN_ID`, `PRP_NET_ID`.
- Existing keys (supervision, duplicate_detection, interlink.forward_all/vlan_filter, multicast_filter) reused with identical WeOS-matching defaults (2s/640ms/64s/64s).
- **Fail-fast validation**: `hsr-prp` requires `lan_id` + `net_id` in 1–6; ring ports must differ from interlink; impossible combos rejected at load.
- **`internal/config` or `internal/prp`: `role.go`** — one module owning port-semantics mapping (which interfaces are ring ports vs PRP LAN ports, role flags), pure and unit-tested, so `if role ==` logic does not scatter across the Node.

## 3 — Frame flows
- **HSR-SAN** (ring A/B + SAN interlink):
  - Ring A|B → check tag → dedup `(src,seq)` → forward to other ring port (PathId 0→1; PathId 1 NOT re-forwarded) → deliver to SAN unless duplicate (multicast filter applies).
  - SAN interlink → add HSR tag (path=0, seq from seqMgr) → send on both ring ports. Own frame returning after a full lap discarded (src-MAC check).
- **HSR-PRP** (ring A/B + one PRP LAN on interlink):
  - PRP LAN → ring: strip RCT, add HSR tag with **PathId = (net_id, lan_id)**, **preserve the RCT seq** end-to-end, send both ring ports.
  - Ring → PRP LAN: dedup, strip HSR tag, **PathId reinjection check per IEC 62439-3 (COR1 2023 §5.2.2.3.1)**: drop the frame iff `PathId.NetId == our NetId && PathId.LanId != our LanId` (frames from the *other* PRP LAN of the same coupled network must never reach our interlink; frames from our own LAN are discarded by the node-table dedup when they complete a lap; frames with a different NetId — pure-HSR ring traffic or another coupled network — are forwarded), add RCT with our lan_id and preserved seq → interlink.
  - **Supervision proxy translation (§5.2.2.3.2)**: an HSR-PRP RedBox translates supervision frames in both directions — HSR supervision (0x88FB, path bits, TLV 22/23) received on the ring is converted to PRP supervision (RCT + lan_id + TLV 20) before forwarding to the interlink LAN, and PRP supervision from the LAN is converted to HSR supervision before injecting into the ring. This is required because DANPs cannot parse HSR supervision frames and DANHs cannot parse PRP supervision frames.

## 4 — Dedup & sequencing
- One nodetable + one seqMgr per source MAC, shared across ring and LAN ports.
- Sequence number preserved across the HSR↔PRP boundary → PRP receivers see identical `(src,seq)` on LAN A vs B → `dupes=0` end-to-end.
- Restart: existing randomized seq start + supervision flush behavior carried over.

## 5 — Supervision
- Ring ports: HSR supervision (path = NetId|LanId for hsr-prp, 0 for hsr-san), REDBOX_MAC TLV → ring liveness + proxy learning.
- PRP LAN port (hsr-prp): existing PRP supervision with RCT, unchanged.

## 6 — GNS3 appliance & TUI
- One appliance, unchanged 3 adapters (eth0=port A, eth1=port B, eth2=interlink); role via `PRP_ROLE` env.
- TUI: mode label, ring port A/B counters, NetId/LanId for hsr-prp. IED appliances unchanged (they are SANs via RedBox SAN ports).

## 7 — Tests (revised)
### 7.0 Reference oracle
- Conformance anchored to Linux kernel `net/hsr` + WeOS semantics (PathId = `(NetId<<1)|LanId`, LSDU size excluding DMAC/SMAC/VLAN tags).
- **Golden byte vectors**: byte-exact HSR frames (plain, VLAN-tagged, supervision, PathId variants) asserted encode→bytes and decode←bytes.

### 7.1 Unit tests (`internal/engine`, `internal/supervision`)
- Tag codec round-trip plain + VLAN-tagged (tag after VLAN tag); EtherType `0x892F`; `IsHSRFrame` vs `IsPRPFrame` cross-contamination guard.
- LSDU size kernel definition (excludes 14-byte header / 18 with VLAN), independent of padding; negative tests (short/truncated/bad length).
- Path semantics: `(NetId<<1)|LanId` for net_id 1–6 × lan A/B (12 vectors); 0→1 forward; 1 never re-forwarded; originator PathId discarded on return.
- Supervision HSR variant build/parse with path bits + REDBOX_MAC TLV; PRP-supervision-on-ring not misparsed.

### 7.2 In-memory topology tests (`internal/prp`, `memPort` style — no Docker)
- **Fake clock**: `Node.now()` injectable (defaults to wall time) → forget-time, supervision timeout, reboot-flush, restart-collision tests run deterministically in ms, no sleeps; run with `-race`.
- Ring of 3–4 HSR-SAN RedBoxes: exactly-once (2 ring laps, 1 delivery); ring-break fault-injection matrix (each link) → 0 loss; multicast full-ring traversal; originator discard.
- HSR-PRP dual RedBox coupling: identical seq on LAN A/B → exactly-once; **PathId reinjection per IEC 62439-3 COR1 §5.2.2.3.1** (frame from LAN A must NOT reappear on LAN B: NetId-mismatch frames forwarded, same-NetId-different-LanId dropped, own-LanId frames discarded by node-table lap detection); supervision proxy translation in both directions; seq preservation & 16-bit wrap; restart seq flush; own-MAC-return drop; malformed tag → drop, no crash; NetId outside 1–6 rejected at load.

### 7.3 Docker integration suite (extends `tests/integration.sh`; keeps 12 PRP tests green)
- HSR-SAN ring: 3 RedBoxes + SAN ping; failover by disconnecting each ring link; reconnect.
- HSR-PRP coupling: two hsr-prp RedBoxes on ring → PRP LAN A/B + existing PRP pair → GOOSE `dupes=0`, SV high-rate, ICMP both directions; LAN-A-down and ring-break failover permutations.
- Supervision-driven restart: kill one ring node mid-traffic → peers flush + recovery (mirrors existing test 8).
- MTU: full-size frames + HSR tag (and VLAN+tag) fit; tshark 0x892F decode sanity if available.

### 7.4 Interop (best-effort, skipped if unavailable)
- Kernel `hsr` in network namespaces: real kernel HSR DAN attached to simulator ring → ICMP/GOOSE both directions. Ultimate standard-compliance check; CI-gated with graceful skip.

### 7.5 Conformance checklist (traceability)
- Each integration case tied to a requirement: path 0→1, PathId reinjection, seq preservation, supervision liveness, tag-after-VLAN, exactly-once.

### 7.6 Engineering enablers (approved)
1. **Fake clock** (7.2) — deterministic, fast tests.
2. **Per-frame trace + drop-reason counters** — `DEBUG_FRAMES=1` prints one line per frame decision (ring port, path, seq, action: fwd/deliver/drop + reason; interlink→ring path tag; own-PathId drop) AND per-interface counters (in/out/dup/own/path/filter/malformed). Trace toggleable: `DEBUG_FRAMES` (existing env) vs separate `--trace` flag — decided: reuse `DEBUG_FRAMES` (single mechanism).
3. **Repo-committed topologies** — `tests/topologies/` docker-compose + GNS3 projects: HSR ring (3 nodes), HSR-PRP dual-RedBox, ring+failover.
4. **Fail-fast config + `role.go`** — load-time validation; single unit-tested role→port-semantics mapping (Section 2).

## 8 — Docs & release
- `prp-standard.md` (replace "HSR not supported" with real HSR + HSR-PRP section), `architecture.md` (modes), `configuration.md` (roles + lan_id/net_id), `README-package.md` + `gns3-setup.md` (HSR topology examples), `troubleshooting.md` (trace + HSR debug guidance).
- New release 0.5.0 + package after completion.

## Out of scope (v1)
- `hsr-hsr` (QuadBox) — deferred, engine kept extensible.
- Multiple rings per LAN (Figure 3 in WeOS HowTo) — NetId range fully validated (1–6) so future multi-ring works.
- WeOS `mode N` (no-forwarding test mode).

## Approval state
- Q1 real HSR wire format ✓; Q2 HSR supervision ✓; Q3 seq reuse + PathId reinjection ✓; Q4 roles A ✓; Q5 full ring traversal ✓; Q6 hsr-hsr deferred ✓; HowTo review ✓; test section revised ✓; engineering enablers 1,2,3,6 ✓.
