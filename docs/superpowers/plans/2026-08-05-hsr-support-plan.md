# Implementation Plan — HSR RedBox + HSR-PRP Coupling (2026-08-05)

References spec: `docs/superpowers/specs/2026-08-05-hsr-support-design.md`
WeOS anchors: docs.westermo.com General/HowTo HSR_PRP; Linux kernel `net/hsr`.

## Phase 0 — Determinism + config foundations
- `internal/prp/prp.go`: add `now func() time.Time` field on Node (default `time.Now`), expose `SetClock(func() time.Time)` for tests; replace direct `time.Now()`/`time.Since` in nodetable/supervision paths where testable.
- `internal/config/config.go`: role validation accepts `prp-san` | `hsr-san` | `hsr-prp` (+ `redbox` alias → `prp-san`); `hsr-prp` requires `interlink.lan_id` (A|B) + `interlink.net_id` (1..6); fail-fast on impossible combos; env `PRP_ROLE`/`PRP_LAN_ID`/`PRP_NET_ID`.
- New `internal/prp/role.go`: single mapping role → port semantics (ringA/ringB/interlink vs lanA/lanB/interlink, isHSR, isCoupling, lanID, netID); pure functions, unit tests.

## Phase 1 — HSR tag codec (`internal/engine`)
- `hsr.go`: `const HSRTagLen=6`, `HSEtherType=0x892F`; `EncodeHSR(payload, path, seq)`, `DecodeHSR(frame) (path, seq, payload, err)`, `IsHSRFrame(frame)`, `IsVLANFrame` reuse; tag AFTER VLAN tag; LSDU size excludes DMAC/SMAC/VLAN tags (kernel def).
- Tests: golden byte vectors (plain/VLAN-tagged/supervision/PathId variants), round-trips, `IsHSRFrame` vs `IsPRPFrame` cross-contamination, negative (short/truncated/bad length).

## Phase 2 — HSR supervision (`internal/supervision`)
- HSR variant: same 0x88FB frame, path bits = NetId|LanId (hsr-prp) or 0 (hsr-san), no RCT on ring; REDBOX_MAC TLV for proxy announcements.
- Tests: build/parse with path bits; PRP-supervision-on-ring not misparsed.

## Phase 3 — `hsr-san` mode (`internal/prp`)
- Node Start: role dispatch; ring A/B = eth0/eth1 raw sockets (existing A/B machinery), interlink = eth2.
- Ring→ring: dedup `(src,seq)`; PathId 0→forward to other port with path=1; PathId 1→no re-forward; deliver to interlink unless duplicate (multicast filter).
- Interlink→ring: seqMgr seq, path=0, send both ring ports; own-return discard via src-MAC check.
- Trace lines + drop-reason counters (per interface: in/out/dup/own/path/filter/malformed) behind `DEBUG_FRAMES=1`.
- Tests: in-memory ring of 3–4 RedBoxes; fault-injection matrix per link; exactly-once; originator discard; `-race`.

## Phase 4 — `hsr-prp` coupling (`internal/prp`)
- PRP LAN→ring: strip RCT, keep seq, add HSR tag path=(net_id<<1|lan_id), send both ring ports.
- Ring→PRP LAN: dedup, strip HSR tag, PathId reinjection check (NetId==ours && LanId!=ours → drop; own returned PathId → drop), add RCT lan_id with preserved seq → interlink.
- Tests: dual-RedBox coupling exactly-once; reinjection negatives (NetId/LanId mismatch); seq preservation + 16-bit wrap; restart flush.

## Phase 5 — TUI + trace polish
- `internal/tui`: mode label (HSR-SAN/PRP-SAN/HSR-PRP), ring port A/B counters, NetId/LanId display; wired from Node counters.
- `DEBUG_FRAMES` (single mechanism) per-frame one-liner; drop-reason counters dumped in TUI.

## Phase 6 — Committed topologies + Docker integration
- `tests/topologies/`: docker-compose + configs for HSR ring (3 nodes), HSR-PRP dual-RedBox coupling, ring+failover.
- `tests/integration.sh` extensions (keep 12 PRP tests green): HSR-SAN ring ping + per-link failover; HSR-PRP coupling GOOSE `dupes=0` + SV high-rate + ICMP both directions; LAN-A-down + ring-break permutations; supervision-driven restart; MTU with HSR tag.
- Best-effort kernel `hsr` netns interop test (skipped when unavailable).

## Phase 7 — Docs + release
- `prp-standard.md` (real HSR + HSR-PRP section), `architecture.md` (modes), `configuration.md` (roles/lan_id/net_id), `README-package.md` + `gns3-setup.md` (HSR examples), `troubleshooting.md` (trace + HSR debug).
- Bump release 0.5.0, tag, build package (`scripts/build-package.sh 0.5.0`).

## Order & commits
Phase 0 → 1 → 2 → 3 → 4 → 5 → 6 → 7, each committed separately. Full unit + integration suite after each phase.
