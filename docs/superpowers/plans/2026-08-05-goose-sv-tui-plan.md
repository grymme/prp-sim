# Implementation Plan — Real GOOSE+SV + ANSI TUI (2026-08-05)

References spec: `docs/superpowers/specs/2026-08-05-goose-sv-tui-design.md`

## Phase 1 — BER + IEC APDUs (`internal/iec`)
- `internal/iec/ber.go`: `encodeTagByte(tag)`, `encodeLength(length int) -> []byte`, `encodeTLV(tag byte, payload []byte) []byte`, `parseTLV(data []byte) (tag byte, payload []byte, rest []byte, ok bool)`. Boolean = `01 01 00` or `01 01 FF`; Integer = 2-byte big-endian (stNum, sqNum, smpCnt, confRev); VisibleString = UTF-8 bytes; UtcTime = `01 08` length + 4B epoch + 3B fraction + 1B flags (0x80).
- `internal/iec/utc.go`: `encodeUtcTime(sec int32) []byte`, `decodeUtcTime(b []byte) (sec int32)`.
- `internal/iec/goose.go`: `buildGOOSEAPDU(appid uint16, stNum uint16, sqNum uint16, t time.Time, test bool, confRev uint16, gcbRef, datSet, goID string) []byte`. Computes header Length = 8 (header) + len(APDU); writes `0x61` BER PDU; includes allData Boolean. `parseGOOSEAPDU(b []byte) (stNum, sqNum uint16, t time.Time, allData bool, ok bool)` walks TLVs, validates tags/length.
- `internal/iec/sv.go`: `buildSVAPDU(appid uint16, smpCnt uint16, confRev uint16, svID string) []byte` (0x60 APDU, ASDU sequence with sample value as int32). `parseSVAPDU(b []byte) (smpCnt uint16, ok bool)`.
- Tests: `internal/iec/ber_test.go` (encode/decode round-trip), `goose_test.go` (header Length ≥8; structure verified by TLV parse; stNum/sqNum/state toggle), `sv_test.go` (mod 4096; ASDU present), `utc_test.go`.

## Phase 2 — Trafficgen rewrite
- `cmd/trafficgen/main.go`: Replace `payloadLen=14` and custom build/parse.
  - `buildFrame(appid, stNum, sqNum, t, test, confRev, vid, pcp, et, dstMAC) []byte`: Ethernet header + real GOOSE/SV header (APPID, Length ≥8, Res1=0, Res2=0) + APDU from Phase 1.
  - `buildFrameEt(...)` kept as a thin wrapper.
  - `parseFrame(frame, wantAppid)`: decode header, call `parseGOOSEAPDU` / `parseSVAPDU`, return (appid, stNum, sqNum, smpCnt, t, allData, vid, pcp, ok).
  - `send`: manage event/retransmit burst with real `stNum`/`sqNum` semantics (`seq++` becomes `stNum` increment on state change, `sqNum` increments per retransmit; `IEC_STEVENTS` controls event interval); SV mode uses `buildSVAPDU`.
  - `recv`: dedup on `(stNum, sqNum)` for GOOSE, `smpCnt` for SV; `stats.noteFrame` updated to accept `(appid, stNum, sqNum, smpCnt, t, allData)`.
  - Flags/env vars: add `IEC_GCB` (default `LLN0$GO$gcb0`), `IEC_DSET` (`LLN0$dataset`), `IEC_GOID` (`LLN0$GO$gcb0`), `IEC_CONFREV` (1), `IEC_SVID` (`MU01`), `IEC_STEVENTS` (10), `IEC_SAMP` (int32 value). `entrypoint.sh` maps `IEC_MODE` to `trafficgen` args; document in header comments.

## Phase 3 — Live ANSI TUI (`internal/tui`)
- `internal/tui/tui.go`: no new dependencies.
  - Detect TTY (`os.Stdout` stat mode bits); full-screen when TTY, one-line when non-TTY.
  - `start()` → goroutine redrawing at 5 Hz: home `\x1b[H`, print 24 lines with ANSI colors, last line blank for scroll back.
  - Publisher screen: `role=IED-Publisher | iface=... | APPID=... | ET=... | rate=... Hz | event=... | stNum=... | sqNum=... | sent=... | uptime=...`.
  - Subscriber screen: `APPID=... | total=... | unique=... | dupes=... | dup%=...% | p50=... | p95=... | max=... | stNum=... | uptime=...`.
  - Restore screen (`\x1b[?1049l`) and print final stats on SIGTERM (same cleanup path in `entrypoint.sh`).
  - Windows VT: check `syscall.Stdout` / `windows.NewLazySystemDLL` and call `SetConsoleMode` with `ENABLE_VIRTUAL_TERMINAL_PROCESSING` (existing `x/sys/windows` dependency handles this).
- Integration into trafficgen: `send()` and `recv()` call `tui.Start()` when `--loop` and stdout is TTY; `tui.Update()` called after each event/frame; `tui.Stop()` called on exit.

## Phase 4 — Integration, docs, package
- `tests/integration.sh`: keep existing 12 tests; add GOOSE PRP run (`dupes=0`). Non-TTY behavior ensures assertions still work.
- `docs/README-package.md`: document real GOOSE/SV format (Wireshark decodes); new env vars; TUI display description.
- `docs/prp-standard.md`: RCT + APDU wire format note.
- `cmd/trafficgen/main.go` header comments: updated to describe real APDU layout.
- `entrypoint.sh`: add `IEC_*` env var documentation in comments; no behavior change.
- Build/release: same `Makefile`; Windows binary rebuilt; package zip rebuilt (existing `scripts/build-package.sh`).

## Dependency check
- No new external packages needed beyond existing `x/sys` and `yaml.v3`.
- `internal/iec` is stdlib only.
- `internal/tui` is stdlib only (ANSI escapes).

## Order of execution
1. Phase 1 (iec package + tests) → 2. Phase 2 (trafficgen rewrite) → 3. Phase 3 (tui + entrypoint) → 4. Phase 4 (tests + docs + build). Each phase committed separately.
