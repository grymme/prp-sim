# PRP GNS3 Simulation Container

A userspace implementation of the **Parallel Redundancy Protocol (PRP, IEC 62439-3)** for use in GNS3 network simulations. Simulates a Westermo-compatible RedBox or DAN node for testing and learning PRP redundancy.

## Features

- Full IEC 62439-3 compliance (RCT trailer, duplicate detection, supervision frames)
- **Dual mode**: RedBox (SAN bridging) or DAN (application node)
- Configurable via YAML file
- GNS3 integration via `.gns3a` appliance
- Lightweight Docker image (~13MB)
- Structured logging and health endpoint for debugging

## Quick Start

### 1. Pull the Docker image

```bash
docker pull ghcr.io/westermo/prp-gns3:latest
```

### 2. Import into GNS3

1. Open GNS3
2. Go to **File → Import appliance**
3. Select `gns3/westermo-prp.gns3a`
4. Follow the wizard (accept defaults)

### 3. Add to your topology

Drag the **Westermo PRP Node** into your workspace and connect:

| Port | Interface | Connects to |
|------|-----------|-------------|
| Port A | eth0 | PRP LAN A |
| Port B | eth1 | PRP LAN B |
| Interlink | eth2 | SAN device / management |

### 4. Start the node

Right-click → Start. The container pulls the image automatically on first use.

## Architecture

```
┌──────────────────────────────────────────┐
│              Docker Container            │
│                                          │
│   eth0 (LAN A) ──┐                       │
│                  │   ┌────────────────┐  │
│                  ├───│   prpd         │  │
│   eth1 (LAN B) ──┘   │  (Go binary)   │  │
│                      │                │  │
│   eth2 (Interlink) ──┤  • RCT         │  │
│                      │  • Dup detect  │  │
│   prp0 (TAP, L2) ◀───┤  • Node table  │  │
│                      │  • Supervision │  │
│                      └────────────────┘  │
└──────────────────────────────────────────┘
```

**Mode behavior:**
- **RedBox**: Bridges Interlink traffic to both PRP LANs, adding/stripping RCT trailers
- **DAN**: Application traffic through `prp0` interface, transparent PRP handling

## Configuration

The container reads `/etc/prp/config.yaml` by default. Override via:

```bash
# Mount custom config
docker run -v /path/to/config.yaml:/etc/prp/config.yaml ...

# Or use environment variables
docker run -e PRP_ROLE=dan -e PRP_PRP_ID=2 ...
```

### Configuration Reference

```yaml
node:
  name: "prp-redbox-1"    # Node name (appears in supervision)
  role: redbox             # redbox | dan

interfaces:
  lan_a: eth0              # PRP LAN A
  lan_b: eth1              # PRP LAN B
  interlink: eth2          # SAN / management interface

virtual_iface:
  name: prp0               # TAP interface for DAN mode
  mac: "auto"              # Auto-derive or explicit MAC

prp:
  prp_id: 1                # PRP network ID (1-6)
  lan_id: "A"              # LAN assignment (A or B)
  suffix: "0x8100"         # RCT suffix (0x8100 or 0xFACE)
  trailer_enabled: true

supervision:
  enabled: true
  life_check_interval: 2s  # Supervision frame interval
  node_forget_time: 64s    # Timeout for stale nodes
  proxy_node_forget_time: 64s

duplicate_detection:
  entry_forget_time: 640ms # How long to remember frames
  max_node_table_size: 256

multicast_filter:
  first_octet: "01-00-5E"  # Multicast filter pattern

interlink:
  mode: san                # san | hsr | prp
  forward_all: true
  vlan_filter: []          # Empty = pass all VLANs
```

See [docs/configuration.md](docs/configuration.md) for full details.

## Docker Usage

### Build locally

```bash
make build
```

### Run standalone

```bash
docker run --rm --privileged --network=host \
  -v $(pwd)/config.yaml:/etc/prp/config.yaml \
  ghcr.io/westermo/prp-gns3:latest
```

### Run with custom role

```bash
docker run --rm --privileged --network=host \
  -e PRP_ROLE=dan \
  -e PRP_PRP_ID=2 \
  ghcr.io/westermo/prp-gns3:latest
```

## GNS3 Integration

### Appliance File

The `.gns3a` file (`gns3/westermo-prp.gns3a`) defines the GNS3 template:

- **3 adapters**: eth0 (LAN A), eth1 (LAN B), eth2 (Interlink)
- **Console**: Telnet (access via GNS3 console or `docker exec`)
- **Image**: Auto-pulled from `ghcr.io/westermo/prp-gns3:latest`

### Example Topology

```
         ┌──────────────┐
         │ PRP LAN A    │
         └──────┬───────┘
                │
    ┌───────────┼───────────┐
    │ eth0      │      eth0 │
┌───┴───┐   ┌───┴───┐   ┌───┴───┐
│RedBox │   │RedBox │   │  SAN  │
│   1   │   │   2   │   │Device │
└───┬───┘   └───┬───┘   └───┬───┘
    │ eth1      │      eth2 │
    │ eth2      │           │
    └───────────┼───────────┘
                │
         ┌──────┴───────┐
         │ PRP LAN B    │
         └──────────────┘
```

See [docs/gns3-setup.md](docs/gns3-setup.md) for detailed setup instructions.

## Development

### Prerequisites

- Go 1.22+
- Docker
- GNS3 (for testing)

### Project Structure

```
cmd/prpd/                  — daemon entrypoint
internal/config/           — YAML config parser
internal/engine/           — RCT encode/decode
internal/nodetable/        — duplicate detection
internal/supervision/      — 0x88fb supervision frames
internal/tap/              — TAP interface management
tests/                     — integration harness
gns3/                      — GNS3 appliance files
docs/                      — documentation
```

### Run Tests

```bash
go test ./...
```

### Build Docker Image

```bash
docker build -t ghcr.io/westermo/prp-gns3:latest .
```

### Integration Test

```bash
cd tests
docker-compose up
```

## Debugging

### Health Status

Access the HTTP status endpoint (if enabled in config):

```bash
curl http://localhost:8080/status
```

Returns JSON with interface states, node table, sequence counters.

### Packet Capture

Enable pcap output for Wireshark analysis:

```bash
docker run --privileged --network=host \
  -v /tmp/prp.pcap:/var/log/prp/capture.pcap \
  ghcr.io/westermo/prp-gns3:latest \
  prpd --pcap=/var/log/prp/capture.pcap
```

### Debug Logging

Enable frame-level logging:

```bash
docker run -e DEBUG_FRAMES=1 -e LOG_FORMAT=json ...
```

See [docs/troubleshooting.md](docs/troubleshooting.md) for common issues.

## Documentation

| Document | Description |
|----------|-------------|
| [Architecture](docs/architecture.md) | System design and data flow |
| [Configuration](docs/configuration.md) | Full config reference |
| [GNS3 Setup](docs/gns3-setup.md) | Step-by-step GNS3 integration |
| [Troubleshooting](docs/troubleshooting.md) | Common issues and solutions |
| [PRP Standard](docs/prp-standard.md) | IEC 62439-3 overview |

## Contributing

1. Fork the repository
2. Create a feature branch
3. Commit changes with clear messages
4. Push and create a Pull Request

## License

This project is provided as-is for educational and testing purposes.

## References

- [IEC 62439-3:2016](https://webstore.iec.ch/en/publication/24566) — PRP and HSR standard
- [Westermo WeOS Documentation](https://docs.westermo.com/weos/) — Real PRP implementation reference
- [GNS3 Documentation](https://docs.gns3.com/) — Network simulation platform
