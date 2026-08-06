# PRP GNS3 Simulation Container

A userspace implementation of the **Parallel Redundancy Protocol (PRP, IEC 62439-3)** for use in GNS3 network simulations. Simulates a Westermo-compatible **RedBox** node for testing and learning PRP redundancy.

## Features

- Full IEC 62439-3 compliance (6-byte RCT trailer / HSR tag, duplicate detection, supervision frames)
- **RedBox modes**: PRP-SAN (SAN into two PRP LANs), HSR-SAN (SAN into an HSR ring), HSR-PRP (dual-RedBox ring↔PRP coupling), HSR-HSR (QuadBox, two HSR rings coupled)
- Configurable via YAML file
- GNS3 integration via `.gns3a` appliance
- Lightweight Docker image (~13MB)
- Structured logging and in-container debugging via telnet console

## Quick Start

### 1. Pull the Docker image

```bash
docker pull ghcr.io/grymme/prp-sim:latest
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
│                      │  • Node table  │  │
│                      │  • Supervision │  │
│                      └────────────────┘  │
└──────────────────────────────────────────┘
```

**Mode behavior:**
- **RedBox**: Bridges Interlink (SAN) traffic to both PRP LANs, adding/stripping RCT trailers and deduplicating on receive

## Configuration

The container reads `/etc/prp/config.yaml` by default. Override via:

```bash
# Mount custom config
docker run -v /path/to/config.yaml:/etc/prp/config.yaml ...

# Or use environment variables (per-node in GNS3)
docker run -e PRP_ROLE=redbox -e PRP_PRP_ID=2 -e PRP_LAN_A_IP=10.0.0.10/24 ...
```

### Configuration Reference

The full commented default config lives in `config.yaml` — every option is
documented inline. Environment variables: `PRP_ROLE`, `PRP_PRP_ID`,
`PRP_LAN_A_IP`, `PRP_LAN_B_IP`, `PRP_INTERLINK_IP`, `DEBUG_FRAMES`.

```yaml
node:
  name: "prp-redbox-1"    # Node name (appears in supervision)
  role: redbox             # redbox

interfaces:
  lan_a: eth0              # PRP LAN A
  lan_b: eth1              # PRP LAN B
  interlink: eth2          # SAN / management interface
  ipv4:                    # Optional static IPs (CIDR), applied at startup
    lan_a: ""
    lan_b: ""
    interlink: ""

prp:
  prp_id: 0                # 0 = auto-derive unique ID from container hostname
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
  first_octet: ""          # e.g. "01-00-5E" to allow IPv4 multicast only

interlink:
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
# Create a dedicated bridge network first (recommended)
make network

docker run --rm --privileged \
  --network prp-sim-bridge \
  -v $(pwd)/config.yaml:/etc/prp/config.yaml \
  ghcr.io/grymme/prp-sim:latest
```

> **Why not `--network=host`?** With host networking the container binds
> its raw sockets to the *host's* real `eth0/eth1/eth2` and raises their
> MTU to 1506 — if those names match your machine's interfaces, the
> simulator will disrupt host networking. Always use a dedicated bridge
> network and connect eth0/eth1/eth2 via GNS3 links or docker networks.

### Run with custom role

```bash
docker run --rm --privileged \
  --network prp-sim-bridge \
  -e PRP_ROLE=redbox \
  -e PRP_PRP_ID=2 \
  -e PRP_LAN_A_IP=10.0.0.10/24 \
  ghcr.io/grymme/prp-sim:latest
```

## GNS3 Integration

### Appliance File

The `.gns3a` file (`gns3/westermo-prp.gns3a`) defines the GNS3 template:

- **3 adapters**: eth0 (LAN A), eth1 (LAN B), eth2 (Interlink)
- **Console**: Telnet (access via GNS3 console or `docker exec`)
- **Image**: Auto-pulled from `ghcr.io/grymme/prp-sim:latest`
- **Default env**: `PRP_ROLE=redbox` (see [docs/gns3-setup.md](docs/gns3-setup.md))

> **Privileged mode**: prpd needs raw sockets (`CAP_NET_RAW`), `/dev/net/tun`
> and interface ioctls. After importing the appliance, open the template
> settings (Edit → Preferences → Docker → Westermo PRP Node) and tick
> **Privileged mode** — see [docs/gns3-setup.md](docs/gns3-setup.md) for
the exact steps.

### Example Topology

A typical PRP deployment uses a RedBox on each side to bridge two SANs across two independent networks (LAN A and LAN B). If either network fails, traffic continues on the other without interruption.

```
    +---------+                             +---------+
    |  SAN 1  |                             |  SAN 2  |
    | (server)|                             | (client)|
    +----+----+                             +----+----+
         |                                      |
    interlink                               interlink
    (eth2)                                  (eth2)
         |                                      |
    +----+----+                             +----+----+
    |         |                             |         |
    | RedBox  |                             | RedBox  |
    |    1    |                             |    2    |
    |         |                             |         |
    +----+----+                             +----+----+
         |    |                                 |     |
    A-port (eth0)                          A-port (eth0)
    B-port (eth1)                          B-port (eth1)
         |    |                                 |     | 
         |    |                                 |     | 
         +-----------LAN A----------------------+     | 
              |                                       | 
              +-----------LAN B-----------------------+



  RedBox 1 sends a copy of SAN 1's frame to both LAN A and LAN B.
  RedBox 2 receives both copies and forwards only one to SAN 2.
  This is the PRP principle: no single network failure can disrupt communication.
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
internal/prp/              — PRP node (RedBox) logic
internal/iface/            — raw sockets, TAP, interface ioctls
internal/nodetable/        — duplicate detection
internal/supervision/      — 0x88fb supervision frames
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
docker build -t ghcr.io/grymme/prp-sim:latest .
```

### Integration Test

```bash
cd tests
docker-compose up
```

## Debugging

### Debug Logging

Enable frame-level logging:

```bash
docker run -e DEBUG_FRAMES=1 ...
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
