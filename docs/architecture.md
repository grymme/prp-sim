# Architecture

## Overview

The PRP GNS3 simulation container implements the Parallel Redundancy Protocol (PRP, IEC 62439-3) in userspace. A single Go binary (`prpd`) manages all PRP operations between two physical LAN interfaces and a virtual TAP interface.

## Components

### prpd (PRP Daemon)

The main process that:
- Reads configuration from YAML
- Creates and manages the TAP interface (`prp0`)
- Binds raw sockets to physical interfaces
- Processes frames in both directions
- Sends supervision frames periodically
- Maintains the node table for duplicate detection

### Engine (`internal/engine/`)

Handles PRP-specific frame processing:
- **RCT Encoding**: Appends 6-byte RCT trailer (seq + LAN ID/LSDU size + 0x88FB suffix) to outgoing frames, padding short frames first
- **RCT Decoding**: Parses and removes RCT from incoming frames
- **Sequence Number Management**: Assigns unique sequence numbers per source MAC

### Node Table (`internal/nodetable/`)

Tracks frames seen on the network for duplicate detection:
- Stores `(source MAC, sequence number)` pairs with expiry timers
- Removes stale entries based on `entry_forget_time`
- Supports up to 256 concurrent entries (configurable)

### Supervision (`internal/supervision/`)

Manages PRP supervision frames (Ethertype `0x88fb`):
- Sends periodic announcements to both LANs
- Advertises node identity and proxy nodes (RedBox mode)
- Responds to supervision from other nodes

### TAP Manager (`internal/tap/`)

Creates and manages the virtual TAP interface:
- Uses Linux TUN/TAP driver (`/dev/net/tun`)
- Configurable MAC address
- Operates at Layer 2 for transparent Ethernet bridging

### Config Parser (`internal/config/`)

Loads and validates YAML configuration:
- Validates required fields (role, interfaces, PRP parameters)
- Supports environment variable overrides
- Provides sensible defaults for missing values

## Data Flow

### Transmit Path (RedBox Mode)

```
┌──────────┐     ┌─────────────────────────────────────┐     ┌──────────┐
│  eth2    │────▶│  1. Read raw Ethernet frame         │────▶│  eth0    │
│(Interlink)     │  2. Insert VLAN tag if present      │     │ (LAN A)  │
└──────────┘     │  3. Assign sequence number          │     └──────────┘
                 │  4. Encode RCT trailer              │
                 │  5. Send to both eth0 and eth1      │     ┌──────────┐
                 └─────────────────────────────────────┘────▶│  eth1    │
                                                             │ (LAN B)  │
                                                             └──────────┘
```

### Receive Path (RedBox Mode)

```
┌──────────┐
│  eth0    │──┐     ┌────────────────────────────────────┐     ┌──────────┐
│ (LAN A)  │  │     │  1. Read frame from either port    │────▶│  eth2    │
└──────────┘  ├────▶│  2. Detect RCT trailer             │     │(Interlink)
┌──────────┐  │     │  3. Extract (src MAC, seq no)      │     └──────────┘
│  eth1    │──┘     │  4. Check node table (dup detect)  │
│ (LAN B)  │        │  5. Strip RCT trailer              │
└──────────┘        │  6. Forward original frame to eth2 │
                    └────────────────────────────────────┘
```

### DAN Mode

```
┌──────────┐     ┌─────────────────────────────────────┐     ┌──────────┐
│  prp0    │────▶│  App writes frame                   │     │  eth0    │
│  (TAP)   │     │  → Insert RCT trailer               │────▶│ (LAN A)  │
└──────────┘     │  → Duplicate to both LANs           │     └──────────┘
                 └─────────────────────────────────────┘     ┌──────────┐
                                                             │  eth1    │
┌──────────┐     ┌─────────────────────────────────────┐     │ (LAN B)  │
│  prp0    │◀────│  Read from eth0/eth1                │◀────└──────────┘
│  (TAP)   │     │  → Strip RCT, dup detect            │
└──────────┘     │  → Write to prp0                    │
                 └─────────────────────────────────────┘
```

## Frame Format

### Ethernet Frame with PRP RCT

```
┌───────────┬───────────┬───────────┬───────────┬───────────┬───────────┐
│ Dst MAC   │ Src MAC   │ EtherType │ Payload   │ RCT       │ FCS       │
│ (6 bytes) │ (6 bytes) │ (2 bytes) │ (46-1500) │ (6 bytes) │ (4 bytes) │
└───────────┴───────────┴───────────┴───────────┴───────────┴───────────┘
                                                      │
                                         ┌────────────┴────────────┐
                                         │ SequenceNo (16 bits)    │
                                         │ LAN ID (4 bits)         │
                                         │ LSDUsize (12 bits)      │
                                         │ Suffix = 0x88FB (16b)   │
                                         └─────────────────────────┘
```

Note: the suffix is the constant `0x88FB` (the PRP EtherType) — this is how a receiver identifies the RCT. Frames are padded to the minimum Ethernet size (60 bytes) before the RCT is appended so the trailer always sits at the end of the wire frame.

### Supervision Frame

```
┌───────────┬───────────┬───────────┬───────────────────────────────┐
│ Dst MAC   │ Src MAC   │ EtherType │ Payload                       │
│ 01-15-4E- │ PRP MAC   │ 0x88fb    │ path+seq, TLV, MacAddressA    │
│ 00-01-00  │           │           │ (+ RCT trailer per LAN)       │
└───────────┴───────────┴───────────┴───────────────────────────────┘
```

## Interfaces

| Interface | Type | Purpose |
|-----------|------|---------|
| `eth0` | Physical (raw socket) | PRP LAN A |
| `eth1` | Physical (raw socket) | PRP LAN B |
| `eth2` | Physical (raw socket) | Interlink / SAN / Management |
| `prp0` | Virtual (TAP) | Application interface (DAN mode) |

## Error Handling

- **Missing interface**: Retry binding with 500ms backoff (up to 10 attempts)
- **Invalid config**: Exit with code 3 and descriptive error message
- **Supervision timeout**: Mark node as stale after `node_forget_time`
- **Duplicate frame**: Silently discard (logged if debug enabled)

## Performance Considerations

- **Userspace overhead**: ~5-10% CPU penalty vs kernel PRP (acceptable for simulation)
- **Memory usage**: ~10MB base + node table (256 entries max)
- **Frame rate**: Limited by raw socket and TAP throughput (~100K pps on modern hardware)
