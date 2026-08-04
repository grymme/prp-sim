# Configuration Reference

## Overview

The PRP daemon reads configuration from `/etc/prp/config.yaml` by default. You can override this with environment variables or by mounting a custom config file. The shipped `config.yaml` (in the repo root, baked into the image) is fully commented and doubles as a tutorial — start there.

## Loading Order

1. **Default path**: `/etc/prp/config.yaml` (built into Docker image)
2. **Environment variable**: Set `PRP_CONFIG_PATH=/path/to/config.yaml`
3. **Docker mount**: `-v /host/config.yaml:/etc/prp/config.yaml`
4. Environment variables override individual values (see table below)

## Environment Variable Overrides

Environment variables take precedence over the config file. This is the
easiest way to customise nodes in GNS3: each GNS3 Docker template can
define per-node environment variables.

| Variable | Config path | Example |
|----------|-------------|---------|
| `PRP_CONFIG_PATH` | (config file path) | `/custom/config.yaml` |
| `PRP_ROLE` | `node.role` | `dan` or `redbox` |
| `PRP_PRP_ID` | `prp.prp_id` | `2` |
| `PRP_LAN_A_IP` | `interfaces.ipv4.lan_a` | `10.0.0.1/24` |
| `PRP_LAN_B_IP` | `interfaces.ipv4.lan_b` | `10.0.0.2/24` |
| `PRP_INTERLINK_IP` | `interfaces.ipv4.interlink` | `10.0.0.3/24` |
| `DEBUG_FRAMES` | (debug logging) | `1` |

## Configuration File Structure

```yaml
node:
  name: "string"       # Required: Node name
  role: "string"       # Required: "redbox" or "dan"

interfaces:
  lan_a: "string"      # Required: LAN A interface name
  lan_b: "string"      # Required: LAN B interface name
  interlink: "string"  # Optional: Interlink interface (RedBox mode)
  ipv4:                # Optional: static IPv4 addresses per port (CIDR)
    lan_a: "10.0.0.1/24"      # empty string = unnumbered
    lan_b: "10.0.0.2/24"
    interlink: "10.0.0.3/24"

virtual_iface:
  name: "string"       # Optional: TAP interface name (default: prp0)
  mac: "string"        # Optional: "auto" (default) or explicit MAC

prp:
  prp_id: integer      # PRP network ID; 0 = auto-derive from hostname
  trailer_enabled: boolean  # Optional: Enable RCT (default: true)

supervision:
  enabled: boolean           # Optional: Enable supervision (default: true)
  life_check_interval: "duration"  # Optional: Send interval (default: "2s")
  node_forget_time: "duration"     # Optional: Peer liveness timeout (default: "64s")
  proxy_node_forget_time: "duration"  # Optional: SAN proxy timeout (default: "64s")

duplicate_detection:
  entry_forget_time: "duration"  # Optional: Frame memory (default: "640ms")
  max_node_table_size: integer    # Optional: Max entries (default: 256)

multicast_filter:
  first_octet: "string"  # Optional: MAC prefix to allow (default: "" = all)

interlink:
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
| `ipv4.lan_a` | string | No | `""` | Optional static IPv4 address (CIDR) for LAN A. IPv4 only. |
| `ipv4.lan_b` | string | No | `""` | Optional static IPv4 address (CIDR) for LAN B. |
| `ipv4.interlink` | string | No | `""` | Optional static IPv4 address (CIDR) for the interlink. |

> **Note on IPs**: prpd is a Layer-2 bridge and never looks at IP
> addresses. Static IPs are purely a convenience (management access, DAN
> application traffic, pinging nodes in a topology). They are applied at
> startup via the `SIOCSIFADDR` ioctls — no `ip` binary needed. IPv6 can
> be configured manually inside the container.

### virtual_iface

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `name` | string | No | `prp0` | Name of the TAP interface created by `prpd`. Applications bind to this interface in DAN mode. |
| `mac` | string | No | `auto` | MAC for the TAP interface. `auto` copies the MAC of `lan_a` (the kernel HSR/PRP behaviour — required for unicast delivery in DAN mode). An explicit MAC must be unique on the network. |

### prp

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `prp_id` | integer | No | `0` | PRP network identifier. `0` derives a unique ID from the container hostname (recommended for GNS3). Otherwise all nodes in the same PRP network must use the same value (1-65535). |
| `trailer_enabled` | boolean | No | `true` | Enable/disable RCT trailer insertion. Disable only for testing. |

### supervision

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `enabled` | boolean | No | `true` | Enable supervision frame transmission. |
| `life_check_interval` | duration | No | `2s` | Interval between supervision frames. Format: `Ns`, `Nms`. |
| `node_forget_time` | duration | No | `64s` | After this time without a supervision frame, a peer is forgotten. |
| `proxy_node_forget_time` | duration | No | `64s` | Lifetime of SAN MACs learned behind the interlink when `forward_all=false`. |

### duplicate_detection

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `entry_forget_time` | duration | No | `640ms` | How long to remember `(src MAC, seq no)` pairs for duplicate detection. |
| `max_node_table_size` | integer | No | `256` | Maximum number of entries in the table. Exceeding this evicts the oldest entry. |

### multicast_filter

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `first_octet` | string | No | `""` | Destination-MAC byte prefix allowed when forwarding multicast (LAN → interlink with `forward_all=false`, or LAN → `prp0` in DAN mode). Hex bytes, e.g. `"01"`, `"01-00-5E"` (IPv4 multicast), `"33-33"` (IPv6). Empty = allow all. Malformed values are rejected at startup. |

### interlink

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `forward_all` | boolean | No | `true` | Forward all frames from the interlink to the PRP LANs. `false` = only forward to MACs learned from the interlink (plus matching multicast/broadcast). |
| `vlan_filter` | [integer] | No | `[]` | List of VLAN IDs to forward from the interlink. Empty = forward all VLANs. |

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
  prp_id: 0
```

### Full DAN Configuration

```yaml
node:
  name: "plc-1"
  role: dan

interfaces:
  lan_a: eth0
  lan_b: eth1
  ipv4:
    lan_a: "10.0.0.10/24"   # management / application IP

virtual_iface:
  name: prp0
  mac: "auto"                # inherits eth0's MAC (required for unicast)

prp:
  prp_id: 0

supervision:
  enabled: true
  life_check_interval: 2s

duplicate_detection:
  entry_forget_time: 640ms
  max_node_table_size: 256
```

### RedBox with static IPs and multicast filtering

```yaml
node:
  name: "redbox-1"
  role: redbox

interfaces:
  lan_a: eth0
  lan_b: eth1
  interlink: eth2
  ipv4:
    interlink: "192.168.1.5/24"   # management access on the SAN side

prp:
  prp_id: 0

multicast_filter:
  first_octet: "01-00-5E"         # forward IPv4 multicast only

interlink:
  forward_all: false              # learn SAN MACs instead of flooding
  vlan_filter: [10, 20]
```

## Validation Rules

- `role` must be `redbox` or `dan`
- `prp_id` must be 0 (auto-derive) or a positive integer
- Static IPs must be valid IPv4 CIDR notation (IPv6 rejected)
- `multicast_filter.first_octet` must be 1-6 hex bytes (e.g. `"01-00-5E"`)
- Interfaces must exist on the system (validated at startup)
- If `role=redbox`, an `interlink` interface is expected

## Troubleshooting Configuration

See [troubleshooting.md](troubleshooting.md) for common configuration errors and solutions.
