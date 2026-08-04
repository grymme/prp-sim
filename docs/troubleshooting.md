# Troubleshooting Guide

## Common Issues

### 1. Container Won't Start

**Symptoms**:
- Node status stays orange in GNS3
- Docker reports "container did not start"
- Console shows no output

**Possible Causes**:

#### Missing Privileges
PRP requires raw socket access. The container must run with `--privileged`.

**Solution**:
```bash
# Test manually with privileged mode (dedicated bridge network, NOT --network=host)
docker run --rm --privileged \
  --network prp-sim-bridge \
  -v $(pwd)/config.yaml:/etc/prp/config.yaml \
  ghcr.io/grymme/prp-sim:latest
```

> **Never use `--network=host`** when testing on a workstation: the
> container binds raw sockets to the host's own `eth0/eth1/eth2` and
> raises their MTU, which disrupts host networking.

#### Interface Doesn't Exist
The config references an interface that doesn't exist on the host.

**Solution**:
```bash
# List available interfaces
ip link show

# Update config.yaml to use existing interfaces
# Example: change eth0/eth1 to docker0/br-xxxxx
```

#### Config File Error
Invalid YAML syntax or missing required fields.

**Solution**:
```bash
# Test config loading (no --config flag; use PRP_CONFIG_PATH)
docker run --rm -v $(pwd)/config.yaml:/etc/prp/config.yaml \
  ghcr.io/grymme/prp-sim:latest \
  prpd 2>&1

# Or point the binary at a custom path
PRP_CONFIG_PATH=/etc/prp/config.yaml prpd

# Check for YAML errors
python3 -c "import yaml; yaml.safe_load(open('config.yaml'))"
```

### 2. No Network Connectivity

**Symptoms**:
- Can't ping between nodes
- No supervision frames visible
- Nodes don't see each other

**Possible Causes**:

#### Wrong Interface Names
Config uses `eth0` but container has different names.

**Solution**:
```bash
# Check interface names inside container
docker exec -it <container> ip link show

# Update config.yaml to match actual interface names
```

#### VLAN Filtering
VLAN filter is blocking traffic.

**Solution**:
```yaml
# In config.yaml, clear VLAN filter
interlink:
  vlan_filter: []  # Empty = pass all
```

#### Network Mode Mismatch
Nodes on different physical networks.

**Solution**:
1. Verify all nodes connect to the same switch
2. Check GNS3 link status (green = connected)
3. Use GNS3's packet capture to verify frames

### 3. Duplicate Detection Issues

**Symptoms**:
- Frames are dropped unexpectedly
- Traffic works intermittently
- Sequence numbers reset frequently

**Possible Causes**:

#### Entry Forget Time Too Short
Duplicate entries expire before duplicate frame arrives.

**Solution**:
```yaml
# Increase entry_forget_time
duplicate_detection:
  entry_forget_time: 1s  # Default: 640ms
```

#### Entry Forget Time Too Long
Table fills up, causing legitimate frames to be dropped.

**Solution**:
```yaml
# Decrease entry_forget_time
duplicate_detection:
  entry_forget_time: 400ms
  max_node_table_size: 512  # Increase if needed
```

#### Node Restarted
A container restart resets the sequence counters, so old duplicate-table
entries may briefly conflict with fresh frames. prpd re-creates the table
at startup, so this clears itself; no configuration needed.

### 4. Supervision Frame Problems

**Symptoms**:
- Nodes don't discover each other
- Proxy nodes not advertised
- Node table stays empty

**Possible Causes**:

#### Supervision Disabled
Config has supervision disabled.

**Solution**:
```yaml
# Enable supervision
supervision:
  enabled: true  # Must be true
```

#### Wrong Multicast Address
Supervision frames sent to wrong multicast address.

**Solution**:
```bash
# Capture supervision frames
sudo tcpdump -i eth0 -e ether proto 0x88fb

# Verify destination MAC starts with 01-15-4E-00-01
```

#### Life Check Interval Mismatch
Nodes use different intervals, causing detection issues.

**Solution**:
```yaml
# Use same interval across all nodes
supervision:
  life_check_interval: 2s  # Standard: 2 seconds
```

### 5. Performance Issues

**Symptoms**:
- High CPU usage
- Dropped frames
- Slow throughput

**Possible Causes**:

#### Excessive Debug Logging
Debug mode enabled in production.

**Solution**:
```bash
# Disable debug logging
DEBUG_FRAMES=0
```

#### Node Table Too Large
Too many entries consuming memory.

**Solution**:
```yaml
# Limit table size
duplicate_detection:
  max_node_table_size: 128  # Reduce from 256
```

#### Raw Socket Overhead
High frame rate overwhelming userspace processing.

**Solution**:
1. Reduce traffic rate
2. Use kernel PRP module (requires different implementation)
3. Run on faster hardware

### 6. GNS3 Integration Issues

**Symptoms**:
- Appliance import fails
- Template not showing in device list
- Docker pull fails

**Possible Causes**:

#### Outdated GNS3 Version
GNS3 2.2+ required for Docker containers.

**Solution**:
```bash
# Check GNS3 version
gns3 --version

# Update GNS3 if needed
```

#### Docker Not Running
Docker service not started or not accessible.

**Solution**:
```bash
# Verify Docker is running
docker ps

# Start Docker if needed
sudo systemctl start docker
```

#### Permission Denied
User not in docker group.

**Solution**:
```bash
# Add user to docker group
sudo usermod -aG docker $USER

# Log out and back in
```

## Debugging Techniques

### Enable Debug Logging

```bash
docker run --rm --privileged \
  --network prp-sim-bridge \
  -e DEBUG_FRAMES=1 \
  ghcr.io/grymme/prp-sim:latest
```

This enables frame-level logging to stdout (logs are plain text).

### Debug Logging Example

```
prp: duplicated frame (seq 42) to both LANs
prp: duplicate frame from aa:bb:cc:dd:ee:ff seq=42 on lan_b, discarding
prp: supervision frame (seq 100) sent on both LANs
```

### Manual Testing

Test individual components:

```bash
# Test config loading (no --config flag; use PRP_CONFIG_PATH)
docker run --rm -v $(pwd)/config.yaml:/etc/prp/config.yaml \
  ghcr.io/grymme/prp-sim:latest \
  prpd 2>&1 | head -20

# Test raw socket binding (dedicated bridge network)
docker run --rm --privileged \
  --network prp-sim-bridge \
  ghcr.io/grymme/prp-sim:latest \
  ip link show

# Test TAP interface
docker run --rm --privileged \
  --network prp-sim-bridge \
  ghcr.io/grymme/prp-sim:latest \
  ip tuntap add dev prp0 mode tap && ip link show prp0
```

## Error Messages

### "error: read config /etc/prp/config.yaml: no such file or directory"

**Cause**: Config file not mounted or wrong path.

**Solution**:
```bash
# Verify file exists
ls -la /path/to/config.yaml

# Mount correctly
docker run -v /path/to/config.yaml:/etc/prp/config.yaml ...
```

### "error: parse config: yaml: unmarshal errors"

**Cause**: Invalid YAML syntax.

**Solution**:
```bash
# Validate YAML
python3 -c "import yaml; yaml.safe_load(open('config.yaml'))"

# Fix syntax errors (indentation, quotes, etc.)
```

### "error: invalid role: xxx (must be redbox or dan)"

**Cause**: Invalid role in config.

**Solution**:
```yaml
# Use valid role
node:
  role: redbox  # or dan
```

### "error: lan_a and lan_b interfaces must be specified"

**Cause**: Missing interface configuration.

**Solution**:
```yaml
# Add both interfaces
interfaces:
  lan_a: eth0
  lan_b: eth1
```

### "tap: failed to create interface"

**Cause**: Missing TUN/TAP device or permissions.

**Solution**:
```bash
# Check if TUN/TAP exists
ls -la /dev/net/tun

# Create if missing (requires root)
sudo mkdir -p /dev/net
sudo mknod /dev/net/tun c 10 200
```

### "raw: failed to bind eth0"

**Cause**: Interface doesn't exist or already bound.

**Solution**:
```bash
# List interfaces
ip link show

# Check if interface is in use
sudo lsof -i eth0

# Update config to use correct interface name
```

## Performance Optimization

### Reduce CPU Usage

```yaml
# Disable debug logging (only the DEBUG_FRAMES env var controls frame logs)
# supervision frames every 5s instead of 2s
supervision:
  life_check_interval: 5s  # Default: 2s
```

### Reduce Memory Usage

```yaml
# Limit node table size
duplicate_detection:
  max_node_table_size: 64  # Default: 256
  entry_forget_time: 400ms  # Default: 640ms
```

### Increase Throughput

```yaml
# Disable VLAN filtering (if not needed)
interlink:
  vlan_filter: []

# Allow all multicast (if needed)
multicast_filter:
  first_octet: ""
```

## Getting Help

### Check Logs

```bash
# View container logs
docker logs <container-id>

# Follow logs in real-time
docker logs -f <container-id>
```

### Community Resources

- [GitHub Issues](https://github.com/grymme/prp-sim/issues)
- [GNS3 Community](https://community.gns3.com)
- [IEC 62439-3 Standard](https://webstore.iec.ch/en/publication/24566)

### Reporting Bugs

When reporting issues, include:

1. **Environment**: OS, GNS3 version, Docker version
2. **Configuration**: Full `config.yaml` (sanitized)
3. **Logs**: Relevant log output
4. **Steps to reproduce**: Exact sequence of actions
5. **Expected vs actual**: What you expected vs what happened
