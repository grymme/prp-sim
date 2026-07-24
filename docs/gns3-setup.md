# GNS3 Setup Guide

## Prerequisites

- GNS3 2.2+ installed
- Docker running (Docker Desktop or Docker Engine)
- GNS3 VM configured (recommended) or local Docker

## Installation Methods

### Method 1: Import Appliance (Recommended)

The `.gns3a` appliance file automates the entire setup.

#### Step 1: Download the Appliance

Clone the repository or download the appliance file:

```bash
git clone https://github.com/grymme/prp-sim.git
```

The appliance file is at: `gns3/westermo-prp.gns3a`

#### Step 2: Import into GNS3

1. Open GNS3
2. Go to **File → Import appliance**
3. Navigate to `gns3/westermo-prp.gns3a`
4. Click **Open**
5. Follow the wizard:
   - Select **Install the appliance on the main computer** (or GNS3 VM)
   - Accept the default settings
   - Click **Finish**

The appliance is now available in the device list.

#### Step 3: Verify Installation

1. Open the **Devices** panel (left sidebar)
2. Look for **Westermo PRP Node** under **End devices** or **Guest**
3. If not visible, restart GNS3

### Method 2: Manual Docker Setup

If the appliance import fails, manually configure the Docker container.

#### Step 1: Pull the Docker Image

```bash
docker pull ghcr.io/westermo/prp-gns3:latest
```

#### Step 2: Create Docker Template

1. Go to **Edit → Preferences → Docker containers**
2. Click **New**
3. Fill in:
   - **Name**: `Westermo PRP Node`
   - **Image**: `ghcr.io/westermo/prp-gns3:latest`
   - **Adapters**: `3`
   - **Console type**: `telnet`
   - **Extra hosts**: (leave empty)
4. Click **Finish**

#### Step 3: Configure Adapter Names

1. Select the new template
2. Click **Edit**
3. Go to **Network** tab
4. Set adapter names:
   - Adapter 0: `eth0 (LAN A)`
   - Adapter 1: `eth1 (LAN B)`
   - Adapter 2: `eth2 (Interlink)`
5. Click **OK**

## Using the PRP Node

### Adding to a Topology

1. Drag **Westermo PRP Node** from the device list to the workspace
2. Repeat for multiple nodes
3. Use the **Add a link** tool to connect interfaces:
   - Click on a node, select an interface
   - Click on another node, select an interface
   - Repeat for all connections

### Typical Topology (PRP Only)

This container simulates PRP only (no HSR). Both LAN A and LAN B are independent star networks with no interconnection.

```
    +-------------+                              +-------------+
    |  Switch A   |                              |  Switch B   |
    | (PRP LAN A) |                              | (PRP LAN B) |
    +------+------+                              +------+------+
           |                                           |
           |                                           |
    +------+------+     +-----------+     +-----+------+
    |   DAN 1     |     |  RedBox   |     |   RedBox 2 |
    | A-port +    |     | A-port +  |     | A-port +   |
    | B-port      |     | B-port +  |     | B-port     |
    +------+------+     |  SAN link |     +-----+------+
           |            +-----+-----+           |
           |                  |                 |
           |                  +-SAN-            |
           |                  (interlink)       |
           |                                    |
           +--------------------+---------------+
                                |
                          (no cross-link
                           between LANs)
```

Each node has two independent connections:
- `eth0` -> Switch LAN A
- `eth1` -> Switch LAN B

Both LANs operate in parallel. If LAN A fails, traffic continues uninterrupted on LAN B.

- A **RedBox** bridges a SAN (via `eth2` interlink) into both PRP LANs.
- A **DAN** talks to LAN A and LAN B directly via its two interfaces. Applications use the `prp0` TAP inside the container.
### Connection Guide

| Node Interface | Connect To | Cable Type |
|----------------|------------|------------|
| Port A (eth0) | PRP LAN A switch | Ethernet |
| Port B (eth1) | PRP LAN B switch | Ethernet |
| Interlink (eth2) | SAN device / management | Ethernet |

### Starting Nodes

1. Right-click on a node
2. Select **Start**
3. Wait for the Docker image to pull (first time only)
4. The node status changes to **running** (green)

### Accessing the Console

1. Right-click on a running node
2. Select **Console**
3. A terminal window opens with the `prpd` daemon

You should see:
```
prpd: role=redbox name=prp-redbox-1 prp_id=1
tap: created interface prp0
raw: bound eth0
raw: bound eth1
supervision: sending 0x88fb on eth0 every 2s
loop: active
```

## Configuring Nodes

### Method 1: Environment Variables

In GNS3, right-click the node → **Configure**:

1. Go to **General settings**
2. In **Environment variables**, add:
   ```
   PRP_ROLE=dan
   PRP_PRP_ID=2
   ```

### Method 2: Custom Config File

1. Create a custom `config.yaml` on your host
2. Mount it into the container:
   - Right-click node → **Configure**
   - Go to **Advanced** → **Extra volumes**
   - Add: `/path/to/config.yaml:/etc/prp/config.yaml`

### Method 3: Edit Inside Container

1. Open the node console
2. Edit the config file:
   ```bash
   vi /etc/prp/config.yaml
   ```
3. Restart the node for changes to take effect

## Testing the Setup

### Basic Connectivity Test

1. Add two RedBox nodes and a SAN device
2. Connect them as shown in the topology diagram
3. Start all nodes
4. From the SAN device console, ping the RedBox IPs:
   ```bash
   ping <redbox-ip>
   ```

### PRP Redundancy Test

1. Start a continuous ping from SAN to RedBox
2. Disconnect LAN A cable
3. Observe: ping continues without interruption
4. Reconnect LAN A, disconnect LAN B
5. Observe: ping continues without interruption

### Supervision Frame Capture

1. Start a node
2. On another machine, capture traffic on LAN A:
   ```bash
   sudo tcpdump -i eth0 -e ether proto 0x88fb
   ```
3. You should see supervision frames every 2 seconds

## Troubleshooting

### Image Pull Fails

**Symptom**: Node shows "Image not found" or pull fails.

**Solution**:
```bash
# Verify Docker is running
docker ps

# Pull manually
docker pull ghcr.io/westermo/prp-gns3:latest

# Check image exists
docker images ghcr.io/westermo/prp-gns3
```

### Node Won't Start

**Symptom**: Node status stays orange/yellow.

**Solution**:
1. Check GNS3 logs: **Help → Debug window**
2. Verify Docker permissions:
   ```bash
   docker run --rm hello-world
   ```
3. Check if interfaces exist:
   ```bash
   docker exec -it <container> ip link show
   ```

### No Supervision Frames

**Symptom**: Nodes don't see each other.

**Solution**:
1. Verify both nodes are on the same LAN
2. Check VLAN settings
3. Enable debug logging:
   ```
   DEBUG_FRAMES=1
   ```
4. Capture frames to verify transmission

### Console Access Fails

**Symptom**: Console window shows "Connection refused".

**Solution**:
1. Ensure node is running
2. Try connecting manually:
   ```bash
   docker exec -it <container> sh
   ```
3. Check if console type matches (telnet vs serial)

## Advanced Configuration

### Multiple PRP Networks

To simulate separate PRP networks:

1. Use different `prp_id` values (1-6)
2. Connect nodes to separate switches
3. Configure supervision intervals appropriately

### HSR-PRP Coupling

To connect HSR and PRP networks:

1. Configure one RedBox with `interlink.mode: prp`
2. Connect the interlink to the PRP network
3. Set appropriate `lan_id` and `prp_id`

### Performance Tuning

For high-traffic simulations:

1. Increase `max_node_table_size`
2. Adjust `entry_forget_time` based on traffic patterns
3. Consider running on the GNS3 VM for better performance

## Additional Resources

- [Configuration Reference](configuration.md)
- [Architecture](architecture.md)
- [Troubleshooting](troubleshooting.md)
- [PRP Standard Overview](prp-standard.md)
