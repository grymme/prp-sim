# PRP GNS3 Simulation

## Quick Start
1. `docker pull ghcr.io/westermo/prp-gns3:latest`
2. In GNS3: **File → Import appliance** → select `gns3/westermo-prp.gns3a`
3. Drag the node into your topology
4. Connect:
   - Port A (eth0) → PRP LAN A
   - Port B (eth1) → PRP LAN B
   - Interlink (eth2) → SAN device
5. Start the node — it pulls the image automatically

## Configuration
Override the default config by mounting your own YAML:
```bash
docker run -v /path/to/config.yaml:/etc/prp/config.yaml ...
```
Or set environment variables:
```
PRP_ROLE=dan
PRP_PRP_ID=2
```
