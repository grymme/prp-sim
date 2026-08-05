# Design — Real IEC 61850 GOOSE + SV, Live TUI, 2026-08-05

## Goal
Trafficgen emits real IEC 61850-8-1 GOOSE (0x88B8) and IEC 61850-9-2 SV (0x88BA) APDUs so Wireshark fully decodes them; publisher/subscriber nodes show a live full-screen ANSI stats view inside the GNS3 main console; the existing GNS3 aux-console (port 5000, telnetd) remains a shell for manual inspection. Scope: real GOOSE, real SV, real TUI, real stNum/sqNum/data-change semantics.

## 1 — Frame formats (new package `internal/iec`)
- `ber.go`: minimal BER encoder (Tlv, long-form length >127, parseTLVs walker, Boolean/Integer/VisibleString/UtcTime helpers). Zero external BER library.
- `goose.go`: 0x61 GOOSE PDU with Table-14 fields (`gocbRef` 80, `timeAllowedToLive` 81, `datSet` 82, `goID` 83, `t` 84 8-byte UtcTime, `stNum` 85, `sqNum` 86, `test` 87, `confRev` 88, `ndsCom` 89, `numDatSetEntries` 8a, `allData` ab Boolean).
- `sv.go`: 0x60 SV APDU (`noASDU` 80, `security` 81 0x0000, `ASDU` a2 with `svID` 80, `smpCnt` 81, `confRev` 82, `smpSynch` 83, `smpRate` 84, `sampl` 85).
- `utc.go`: encode/decode 8-byte UtcTime (4B epoch-sec, 3B fraction, 1B 0x80 flags).
- Ethernet header (replacing `buildFrame`): dst 01-0C-CD-01-xx-xx, src, [802.1Q tag (0x8100) when VID/PCP configured], EtherType 0x88B8/0x88BA, 8-byte GOOSE/SV header (APPID 2B, Length 2B ≥8, Res1 2B=0, Res2 2B=0), APDU.

## 2 — Publisher/subscriber behavior
- **Publisher send**: GOOSE retransmits same `stNum` with increasing `sqNum` (2,4,8,16,32,64,128,256ms then 1s steady) until new event (state-change, default every 10 retransmissions, `IEC_STEVENTS`); new event increments `stNum`, resets `sqNum`, toggles `allData` Boolean.
- SV: `smpCnt` increments mod 4096 at `--rate` Hz.
- **Subscriber recv**: dedup key GOOSE `(stNum, sqNum)`, SV `smpCnt`; latency from APDU `t` (GOOSE) or arrival-time delta (SV); stats unchanged (total/unique/dupes/percentiles).

## 3 — Live TUI (`internal/tui`) & appliance wiring
- Pure stdlib ANSI (`\x1b[H` home, colors, lines 1..24). Zero dependencies.
- Publisher screen: role, interface, APPID, EtherType, rate, stNum, sqNum, sent, uptime.
- Subscriber screen: APPID, total/unique/dupes, dup ratio, latency min/p50/p95/max, last stNum or smpCnt seen, uptime.
- TTY check: full-screen when stdout is terminal; non-TTY (integration harness, pipes) falls back to existing one-line periodic `stats.partial()`.
- Windows: `golang.org/x/sys/windows` VT-mode enable (existing dependency).
- GNS3 aux shell: entrypoint's `telnetd -l /bin/sh -p 5000` stays the shell; main console is the TUI. New env vars (`IEC_GCB`, `IEC_DSET`, `IEC_GOID`, `IEC_CONFREV`, `IEC_STEVENTS`, `IEC_SVID`) flow through `entrypoint.sh` to trafficgen flags; appliance JSON unchanged.

## 4 — Testing & docs
- Unit: `internal/iec` round-trip; header `Length` ≥8; real GOOSE structure verified by a `parseTLVs` decode of a generated frame; SV `smpCnt` mod 4096; malformed rejection; TUI non-TTY fallback.
- Integration: `tests/integration.sh` — existing 12 tests green; add PRP publisher→subscriber asserting `dupes=0` with real APDUs.
- Docs: `docs/prp-standard.md` (RCT + real wire format notes), `docs/README-package.md` (env vars, TUI, Wireshark note), `cmd/trafficgen/main.go` header comment, `entrypoint.sh` comments.

## Approach decisions
- Real formats not legacy `payloadLen=14` custom format; subscriber parser rewritten to decode BER APDU.
- SV implemented to same BER standard; scope covers both.

## Approval state
- Sections 1, 2, 3, 4 approved by user 2026-08-05.
