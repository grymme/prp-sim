package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"prp-gns3/internal/config"
	"prp-gns3/internal/iface"
	"prp-gns3/internal/prp"
)

func main() {
	configPath := "/etc/prp/config.yaml"
	if p := os.Getenv("PRP_CONFIG_PATH"); p != "" {
		configPath = p
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("prpd: role=%s name=%s prp_id=%d\n", cfg.Node.Role, cfg.Node.Name, cfg.PRP.PRPID)

	// Build PRP configuration
	prpCfg := &prp.Config{
		NodeName:            cfg.Node.Name,
		Role:                cfg.Node.Role,
		LanAInterface:       cfg.Interfaces.LanA,
		LanBInterface:       cfg.Interfaces.LanB,
		InterlinkInterface:  cfg.Interfaces.Interlink,
		PRPID:               cfg.PRP.PRPID,
		TrailerEnabled:      cfg.PRP.TrailerEnabled,
		Debug:               os.Getenv("DEBUG_FRAMES") == "1",
		ForwardAll:          cfg.Interlink.ForwardAll,
		VLANFilter:          cfg.Interlink.VLANFilter,
		MulticastFirstOctet: cfg.MulticastFilter.FirstOctet,
	}
	if d, err := time.ParseDuration(cfg.DuplicateDetection.EntryForgetTime); err == nil && d > 0 {
		prpCfg.EntryForgetMs = int(d / time.Millisecond)
	}
	// supervision.proxy_node_forget_time: lifetime of SAN MACs learned
	// behind the interlink (used when interlink.forward_all is false).
	if d, err := time.ParseDuration(cfg.Supervision.ProxyNodeForgetTime); err == nil && d > 0 {
		prpCfg.ProxyNodeForgetMs = int(d / time.Millisecond)
	}
	// supervision.node_forget_time: how long a peer stays alive in the
	// node table after its last supervision frame.
	if d, err := time.ParseDuration(cfg.Supervision.NodeForgetTime); err == nil && d > 0 {
		prpCfg.NodeForgetMs = int(d / time.Millisecond)
	}
	prpCfg.MaxNodeTableSize = cfg.DuplicateDetection.MaxNodeTableSize

	// Apply optional static IPv4 addresses from the config (interfaces.ipv4)
	// or the PRP_*_IP environment variables. The PRP engine itself is
	// Layer-2, so this is purely for convenience (management access,
	// debugging). Failures are logged but do not stop
	// the node.
	applyStaticIPs(cfg)

	// Create and start the PRP node
	node := prp.NewNode(prpCfg)
	if err := node.Start(); err != nil {
		log.Fatalf("prp: failed to start node: %v", err)
	}

	// Set up signal handling
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	// Set up supervision frame sender if enabled
	if cfg.Supervision.Enabled {
		go func() {
			interval := 2 * time.Second
			if cfg.Supervision.LifeCheckInterval != "" {
				if d, err := time.ParseDuration(cfg.Supervision.LifeCheckInterval); err == nil {
					interval = d
				}
			}

			ticker := time.NewTicker(interval)
			defer ticker.Stop()

			for {
				select {
				case <-ticker.C:
					node.SendSupervisionFrame()
				case <-node.StopChan():
					return
				}
			}
		}()
	}

	// Wait for signal
	<-sig
	fmt.Println("prpd: shutting down")
	node.Stop()
}

// applyStaticIPs assigns the optional per-port IPv4 addresses from the
// configuration. The PRP engine forwards Ethernet frames and never looks at
// IPs, so this is a convenience feature: give the node management
// connectivity without hand-running ip(8) in every container.
func applyStaticIPs(cfg *config.Config) {
	for _, p := range []struct {
		port string
		name string
		cidr string
	}{
		{"LAN A", cfg.Interfaces.LanA, cfg.Interfaces.IPv4.LanA},
		{"LAN B", cfg.Interfaces.LanB, cfg.Interfaces.IPv4.LanB},
		{"Interlink", cfg.Interfaces.Interlink, cfg.Interfaces.IPv4.Interlink},
	} {
		if p.name == "" || p.cidr == "" {
			continue
		}
		// Ensure the link is up so the address becomes usable.
		if err := iface.SetInterfaceUp(p.name); err != nil {
			log.Printf("prp: warning: could not bring up %s: %v", p.name, err)
		}
		if err := iface.SetInterfaceIP(p.name, p.cidr); err != nil {
			log.Printf("prp: warning: could not set %s = %s on %s: %v", p.port, p.cidr, p.name, err)
			continue
		}
		log.Printf("prp: set static IP %s on %s (%s)", p.cidr, p.name, p.port)
	}
}
