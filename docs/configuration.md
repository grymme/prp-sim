# Configuration Reference

## Overview

The PRP daemon reads configuration from `/etc/prp/config.yaml` by default. You can override this with environment variables or by mounting a custom config file.

## Loading Order

1. **Default path**: `/etc/prp/config.yaml` (built into Docker image)
2. **Environment variable**: Set `PRP_CONFIG_PATH=/path/to/config.yaml`
3. **Docker mount**: `-v /host/config.yaml:/etc/prp/config.yaml`
4. **CLI flag**: `prpd --config=/path/to/config.yaml` (if implemented)

## Environment Variable Overrides

Override individual config values via environment variables:

| Variable | Config Path | Example |
|----------|-------------|---------|
| `PRP_CONFIG_PATH` | (config file path) | `/custom/config.yaml` |
| `PRP_ROLE` | `node.role` | `dan` or `redbox` |
| `PRP_PRP_ID` | `prp.prp_id` | `2` |
| `PRP_LAN_ID` | `prp.lan_id` | `B` |
| `DEBUG_FRAMES` | (debug logging) | `1` |
| `LOG_FORMAT` | (log output format) | `json` |

## Configuration File Structure

```yaml
node:
  name: "string"       # Required: Node name
  role: "string"       # Required: "redbox" or "dan"

interfaces:
  lan_a: "string"      # Required: LAN A interface name
  lan_b: "string"      # Required: LAN B interface name
  interlink: "string"  # Optional: Interlink interface (RedBox mode)

virtual_iface:
  name: "string"       # Optional: TAP interface name (default: prp0)
  mac: "string"        # Optional: MAC address or "auto" (default: auto)

prp:
  prp_id: integer      # Required: PRP network ID (1-6)
  lan_id: "string"     # Required: "A" or "B"
  suffix: "string"     # Optional: RCT suffix (default: "0x8100")
  trailer_enabled: boolean  # Optional: Enable RCT (default: true)

supervision:
  enabled: boolean           # Optional: Enable supervision (default: true)
  life_check_interval: "duration"  # Optional: Send interval (default: "2s")
  node_forget_time: "duration"     # Optional: Stale node timeout (default: "64s")
  proxy_node_forget_time: "duration"  # Optional: Proxy timeout (default: "64s")
  node_reboot_interval: "duration"    # Optional: Reboot detection (default: "500ms")

duplicate_detection:
  entry_forget_time: "duration"  # Optional: Frame memory (default: "640ms")
  max_node_table_size: integer    # Optional: Max entries (default: 256)

multicast_filter:
  first_octet: "string"  # Optional: Filter pattern (default: "01-00-5E")

interlink:
  mode: "string"         # Optional: "san", "hsr", or "prp" (default: "san")
  forward_all: boolean   # Optional: Forward all frames (default: true)
  vlan_filter: [integer] # Optional: VLAN IDs to forward (empty = all)
```

## Detailed Parameter Reference

### node

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `name` | string | Yes | — | Human-readable node name. Appears in supervision frames and logs. |
| `role` | string | Yes | — | Operating mode. `redbox` bridges SAN traffic to PRP LANs. `dan` provides PRP access to applications via `prp0`. |

### interfaces

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `lan_a` | string | Yes | — | Network interface connected to PRP LAN A. Used for raw socket binding. |
| `lan_b` | string | Yes | — | Network interface connected to PRP LAN B. Used for raw socket binding. |
| `interlink` | string | No | — | Network interface for SAN/management traffic. Required for RedBox mode. Ignored in DAN mode. |

### virtual_iface

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `name` | string | No | `prp0` | Name of the TAP interface created by `prpd`. Applications bind to this interface in DAN mode. |
| `mac` | string | No | `auto` | MAC address for the TAP interface. `auto` derives from `eth0`. Example: `"02:42:ac:1a:00:01"`. |

### prp

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `prp_id` | integer | Yes | — | PRP network identifier (1-6). Must match across all nodes in the same PRP network. |
| `lan_id` | string | Yes | — | Which LAN this node connects to. `A` for LAN A, `B` for LAN B. Used in HSR-PRP coupling. |
| `suffix` | string | No | `0x8100` | RCT suffix value. Standard: `0x8100`. Some implementations use `0xFACE`. Must match across network. |
| `trailer_enabled` | boolean | No | `true` | Enable/disable RCT trailer insertion. Disable only for testing. |

### supervision

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `enabled` | boolean | No | `true` | Enable supervision frame transmission. |
| `life_check_interval` | duration | No | `2s` | Interval between supervision frames. Format: `Ns`, `Nms`. |
| `node_forget_time` | duration | No | `64s` | Time after which a node is considered stale if no supervision received. |
| `proxy_node_forget_time` | duration | No | `64s` | Time after which proxy nodes (behind RedBox) are forgotten. |
| `node_reboot_interval` | duration | No | `500ms` | Time window to detect node reboots via sequence number reset. |

### duplicate_detection

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `entry_forget_time` | duration | No | `640ms` | How long to remember `(src MAC, seq no)` pairs for duplicate detection. |
| `max_node_table_size` | integer | No | `256` | Maximum number of entries in the node table. Exceeding this drops oldest entries. |

### multicast_filter

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `first_octet` | string | No | `01-00-5E` | Filter multicast frames based on first octet of destination MAC. `00` = forward all. |

### interlink

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `mode` | string | No | `san` | Interlink operating mode. `san` = single-attached node. `hsr` = HSR ring. `prp` = PRP network. |
| `forward_all` | boolean | No | `true` | Forward all frames from Interlink to PRP LANs. `false` = only forward to known unicast. |
| `vlan_filter` | [integer] | No | `[]` | List of VLAN IDs to forward. Empty = forward all VLANs. |

## Examples

### Minimal RedBox Configuration

```yaml
node:
  name: "redbox-1"
  role: redbox

interfaces:
  lan_a: eth0
  lan_b: eth1
  interlink: eth2

prp:
  prp_id: 1
  lan_id: "A"
```

### Full DAN Configuration

```yaml
node:
  name: "plc-1"
  role: dan

interfaces:
  lan_a: eth0
  lan_b: eth1

virtual_iface:
  name: prp0
  mac: "02:42:ac:1a:00:01"

prp:
  prp_id: 1
  lan_id: "A"
  suffix: "0x8100"
  trailer_enabled: true

supervision:
  enabled: true
  life_check_interval: 2s
  node_forget_time: 64s

duplicate_detection:
  entry_forget_time: 640ms
  max_node_table_size: 256
```

### HSR-PRP Coupling (RedBox connecting HSR ring to PRP)

```yaml
node:
  name: "coupling-box"
  role: redbox

interfaces:
  lan_a: eth0      # Connects to PRP LAN A
  lan_b: eth1      # Connects to HSR ring
  interlink: eth2  # Connects to PRP LAN B

prp:
  prp_id: 1
  lan_id: "A"      # Mark as LAN A for PRP network

interlink:
  mode: prp        # Interlink operates in PRP mode
  forward_all: true
```

## Validation Rules

- `role` must be `redbox` or `dan`
- `prp_id` must be 1-6 (0 reserved for HSR-SAN, 7 reserved)
- `lan_id` must be `A` or `B`
- `suffix` must be `0x8100` or `0xFACE`
- Interfaces must exist on the system (validated at startup)
- If `role=redbox`, `interlink` interface is required

## Troubleshooting Configuration

See [troubleshooting.md](troubleshooting.md) for common configuration errors and solutions.
