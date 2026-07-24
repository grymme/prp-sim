# PRP Standard Overview (IEC 62439-3)

## Introduction

The **Parallel Redundancy Protocol (PRP)** is defined in **IEC 62439-3:2016** — "Industrial communication networks – High availability automation networks – Part 3: Parallel Redundancy Protocol (PRP) and High-availability Seamless Redundancy (HSR)".

PRP provides seamless redundancy by transmitting duplicate frames over two independent local area networks (LAN A and LAN B). The receiving node accepts the first frame and discards the duplicate, ensuring zero recovery time on network failures.

## Key Concepts

### Doubly Attached Node (DAN)

A device with two network interfaces connected to both LAN A and LAN B. DANs natively support PRP and can send/receive duplicate frames.

**Example**: A PLC or RTU with built-in PRP support.

### Single Attached Node (SAN)

A device with only one network interface. SANs cannot participate directly in PRP but can connect through a RedBox.

**Example**: A legacy device or a standard computer.

### Redundancy Box (RedBox)

A device that bridges a SAN into the PRP network. The RedBox:
- Adds PRP trailers to frames from the SAN
- Duplicates frames to both LANs
- Removes trailers and discards duplicates for frames going to the SAN

**Example**: Westermo Lynx-RB or our simulation container in RedBox mode.

### Interlink

The connection between a RedBox and a SAN. Operates at Layer 2, transparent to the SAN.

## PRP Frame Format

### Ethernet Frame Structure

```
┌─────────────────────────────────────────────────────────────────┐
│ Ethernet Frame with PRP RCT                                     │
├───────────┬───────────┬───────────┬───────────┬─────────────────┤
│ Dst MAC   │ Src MAC   │ EtherType │ Payload   │ RCT             │
│ (6 bytes) │ (6 bytes) │ (2 bytes) │ (46-1500) │ (4 bytes)       │
└───────────┴───────────┴───────────┴───────────┴─────────────────┘
```

### RCT (Redundancy Control Trailer)

The 4-byte RCT is appended to every PRP frame:

```
┌─────────────────────────────────────────────────────────────────┐
│ RCT Structure (4 bytes)                                         │
├─────────────────────┬─────────┬─────────────┬───────────────────┤
│ Sequence Number     │ LAN ID  │ LSDU Size   │ Suffix            │
│ (16 bits)           │ (4 bits)│ (12 bits)   │ (16 bits)         │
└─────────────────────┴─────────┴─────────────┴───────────────────┘
```

**Fields**:
- **Sequence Number**: 16-bit counter, incremented per source MAC. Used for duplicate detection.
- **LAN ID**: Identifies which LAN the frame was sent on (A=1, B=2).
- **LSDU Size**: Length of the payload (including RCT). Used to locate the trailer.
- **Suffix**: Identifies the RCT. Standard: `0x8100`. Some implementations use `0xFACE`.

### Frame Transmission

When a DAN or RedBox sends a frame:

1. Assign a sequence number (per source MAC)
2. Append the RCT trailer
3. Send the modified frame to both LAN A and LAN B
4. Both copies travel independently through the network

### Frame Reception

When a DAN or RedBox receives a frame:

1. Detect the RCT trailer (check suffix)
2. Extract the sequence number and LAN ID
3. Look up `(source MAC, sequence number)` in the node table
4. If found: discard (duplicate)
5. If not found: accept the frame, add to node table, forward to application/SAN

## Supervision Frames

### Purpose

Supervision frames (Ethertype `0x88fb`) are used for:
- Node discovery
- Proxy node advertisement (RedBox)
- Network health monitoring
- Reboot detection

### Frame Format

```
┌─────────────────────────────────────────────────────────────────┐
│ Supervision Frame                                               │
├───────────┬───────────┬───────────┬─────────────────────────────┤
│ Dst MAC   │ Src MAC   │ EtherType │ Payload                     │
│ (6 bytes) │ (6 bytes) │ 0x88fb    │ (variable)                  │
└───────────┴───────────┴───────────┴─────────────────────────────┘
```

### Payload Contents

- **PRP-ID**: Network identifier (1-6)
- **LAN-ID**: Which LAN this frame was sent on
- **Node State**: Active, Inactive, etc.
- **Sequence Number**: Monotonic counter for supervision
- **Node Count**: Number of nodes behind this RedBox
- **Node List**: MAC addresses of nodes behind the RedBox (proxy nodes)

### Timing

- **Life Check Interval**: How often supervision frames are sent (default: 2 seconds)
- **Node Forget Time**: How long to remember a node without supervision (default: 64 seconds)
- **Proxy Node Forget Time**: How long to remember proxy nodes (default: 64 seconds)

## Duplicate Detection

### Node Table

Each PRP node maintains a table of seen frames:

```
┌─────────────────────┬─────────────┬─────────┬─────────────┬───────────┐
│ Source MAC          │ Sequence No │ LAN ID  │ Last Seen   │ Expiry    │
├─────────────────────┼─────────────┼─────────┼─────────────┼───────────┤
│ aa:bb:cc:dd:ee:ff   │ 42          │ A       │ 10:00:01    │ 10:00:02  │
│ aa:bb:cc:dd:ee:ff   │ 43          │ A       │ 10:00:01    │ 10:00:02  │
│ 11:22:33:44:55:66   │ 100         │ B       │ 10:00:01    │ 10:00:02  │
└─────────────────────┴─────────────┴─────────┴─────────────┴───────────┘
```

### Detection Algorithm

1. Receive frame from LAN A or LAN B
2. Extract `(source MAC, sequence number)` from RCT
3. Look up in node table:
   - **Found**: Duplicate → discard
   - **Not found**: Accept frame, add to table
4. Remove expired entries based on `entry_forget_time`

### Parameters

- **Entry Forget Time**: How long to remember a frame (default: 640ms)
- **Max Table Size**: Maximum entries (default: 256)

## Node Table Management

### Entry Creation

When a new frame is received:
1. Check if `(source MAC, sequence number)` exists
2. If not, create entry with current timestamp
3. Set expiry based on `entry_forget_time`

### Entry Expiry

Entries are removed when:
- Current time > entry timestamp + `entry_forget_time`
- Table exceeds `max_node_table_size` (oldest entries removed)

### Reboot Detection

If a node's sequence number resets to 0 (or a low value):
1. Node may have rebooted
2. Old entries for that MAC are invalidated
3. New sequence numbers are accepted

## Multicast Handling

### Default Behavior

Multicast frames are duplicated to both LANs, same as unicast.

### Filtering

Some implementations filter multicast based on destination MAC:

- **First Octet Filter**: Check first byte of destination MAC
- **VLAN Filter**: Forward only specific VLANs
- **Protocol Filter**: Forward only specific EtherTypes

### Configuration

```yaml
multicast_filter:
  first_octet: "01-00-5E"  # Standard multicast prefix
  vlan_filter: []           # Empty = forward all
```

## HSR-PRP Coupling

### Overview

HSR (High-availability Seamless Redundancy) and PRP can be coupled using special RedBoxes:

- **HSR-PRP RedBox**: Connects an HSR ring to a PRP network
- **QuadBox**: Connects two HSR rings or PRP networks

### Coupling Rules

1. Frames entering from PRP to HSR get a PathId added
2. Frames entering from HSR to PRP get RCT trailer
3. Duplicate detection applies across both networks
4. PRP-ID must match within the same network

### Configuration Example

```yaml
node:
  name: "hsr-prp-coupling"
  role: redbox

interfaces:
  lan_a: eth0      # PRP LAN A
  lan_b: eth1      # HSR ring
  interlink: eth2  # PRP LAN B

prp:
  prp_id: 1
  lan_id: "A"

interlink:
  mode: prp        # Interlink operates in PRP mode
```

## Network Topologies

### Dual Parallel LANs (Standard PRP Topology)

PRP uses two completely independent LANs (A and B). There is no ring between them. Nodes connect to both LANs in parallel. If one LAN fails, traffic continues uninterrupted on the other.

```
    +-------------+                              +-------------+
    |  Switch A   |                              |  Switch B   |
    |  (PRP LAN A)|                              | (PRP LAN B) |
    +------+------+                              +------+------+
           |                                           |
           |                                           |
    +------+------+    +----------+    +---------+     +------+------+
    |    DAN 1    |    |  DAN 2   |    | RedBox  |     |  RedBox 2   |
    |  A  +  B    |    | A  +  B  |    | A + B   |     |  A  +  B   |
    |    ports    |    |  ports   |    | + SAN   |     |   ports    |
    +------+------+    +----+-----+    +----+----+     +-----+-------+
           |                |               |                |
           |                |               +-SAN            |
           |                |               (interlink)      |
           |                |                |                |
           +----------------+----------------+----------------+
                          (no cross-link between LANs)
```

**Key facts:**

- **Two independent star networks** (LAN A and LAN B). No switch or router connects them.
- **Each DAN connects to both LANs** simultaneously, sending duplicate frames on each.
- **A RedBox bridges a SAN** (or a non-PRP device) into both PRP LANs via its interlink port.
- **Failure isolation**: a failure on LAN A is invisible to applications on the SAN side - traffic simply continues on LAN B.

Common PRP deployments:

- **DAN to DAN direct**: both devices connected directly to LAN A and LAN B. Simplest topology.
- **DAN and SAN via RedBox**: a SAN connects through the RedBox interlink to both LANs.
- **Two SANs via two RedBoxes**: each SAN behind its own RedBox on the same PRP network.

### Note on HSR

This container implements PRP only. HSR (High-availability Seamless Redundancy) uses a different topology (a ring, not two parallel LANs) and a different frame-tagging mechanism. Coupling an HSR ring with a PRP network requires a special QuadBox device and is out of scope for this implementation.

For HSR simulation, use a separate HSR simulator.

## Performance Characteristics

### Latency

- **Zero recovery time**: No interruption on link failure
- **Frame delay**: Minimal (~1-2 µs for RCT processing)
- **Duplicate detection**: O(1) lookup in node table

### Throughput

- **Theoretical**: 2x bandwidth (both LANs active)
- **Practical**: Limited by slowest LAN
- **Overhead**: ~4 bytes per frame (RCT)

### Reliability

- **Failover**: Seamless (no packet loss)
- **Single point of failure**: None (dual paths)
- **Recovery time**: 0 ms

## Comparison with HSR

| Feature | PRP | HSR |
|---------|-----|-----|
| **Topology** | Star (dual LAN) | Ring |
| **Redundancy** | Parallel | Ring-based |
| **Recovery Time** | 0 ms | 0 ms |
| **Bandwidth** | 2x (both LANs) | 1x (ring) |
| **Frame Size** | +4 bytes (RCT) | +4 bytes (HSR tag) |
| **Standard** | IEC 62439-3 | IEC 62439-3 |
| **Use Case** | Substation automation | Industrial networks |

## References

- **IEC 62439-3:2016**: Industrial communication networks – High availability automation networks – Part 3
- **IEC 61850**: Communication networks and systems for power utility automation
- **IEEE 1588**: Precision Time Protocol (PTP)
- **Westermo WeOS Documentation**: Real-world PRP implementation reference

## Further Reading

- [Architecture](architecture.md) - System design details
- [Configuration](configuration.md) - Full config reference
- [GNS3 Setup](gns3-setup.md) - Practical deployment guide
- [Troubleshooting](troubleshooting.md) - Common issues and solutions
