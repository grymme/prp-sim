package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"prp-gns3/internal/config"
	"prp-gns3/internal/iface"
	"prp-gns3/internal/prp"
	"prp-gns3/internal/tui"
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
		LanID:               cfg.HSR.LanID,
		NetID:               cfg.HSR.PRPID,
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

	// Route prpd's log output through a bounded ring buffer. When the
	// console is a terminal the TUI renders the buffer's last lines in
	// its debug panel; the buffer also tees everything to stderr so
	// `docker logs` keeps the full output.
	ring := tui.NewRingBuffer(200, func(p []byte) { os.Stderr.Write(p) })
	log.SetOutput(ring)

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

	// Console presentation: a full-screen ANSI TUI when stdout is a real
	// terminal (GNS3 telnet console) and PRP_NO_TUI is not set; otherwise
	// the plain one-line status (CI, pipes, `docker logs`).
	if tui.IsTerminal(1) && os.Getenv("PRP_NO_TUI") == "" {
		go runTUILoop(node, ring, cfg)
	} else {
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					st := node.Snapshot()
					role := st.Role
					if role == "hsr-prp" {
						role = fmt.Sprintf("hsr-prp (NetId %d, LanId %s)", st.NetID, st.LanID)
					}
					fmt.Printf("status: role=%s ringA in=%d out=%d ringB in=%d out=%d inter in=%d out=%d sup=%d ntable=%d drops=%v\n",
						role,
						st.LanAIn, st.LanAOut, st.LanBIn, st.LanBOut,
						st.InterIn, st.InterOut, st.SupSent, st.DupTableSize,
						formatDrops(st.Drops))
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

// runTUILoop renders the full-screen TUI on a 10 s ticker.
func runTUILoop(node *prp.Node, ring *tui.RingBuffer, cfg *config.Config) {
	view := tui.StartPrpd(os.Stdout)
	defer view.StopPrpd()

	interfaces := fmt.Sprintf("%s (A), %s (B), %s (interlink)",
		cfg.Interfaces.LanA, cfg.Interfaces.LanB, cfg.Interfaces.Interlink)
	vlan := "none"
	if len(cfg.Interlink.VLANFilter) > 0 {
		parts := make([]string, 0, len(cfg.Interlink.VLANFilter))
		for _, v := range cfg.Interlink.VLANFilter {
			parts = append(parts, fmt.Sprintf("%d", v))
		}
		vlan = strings.Join(parts, ",")
	}
	mc := "none (allow all)"
	if cfg.MulticastFilter.FirstOctet != "" {
		mc = cfg.MulticastFilter.FirstOctet
	}
	sup := "off"
	if cfg.Supervision.Enabled {
		sup = "on (" + cfg.Supervision.LifeCheckInterval + ")"
		if sup == "on ()" {
			sup = "on (2s)"
		}
	}
	entryForget := cfg.DuplicateDetection.EntryForgetTime
	if entryForget == "" {
		entryForget = "640ms"
	}
	nodeForget := cfg.Supervision.NodeForgetTime
	if nodeForget == "" {
		nodeForget = "64s"
	}
	proxyForget := cfg.Supervision.ProxyNodeForgetTime
	if proxyForget == "" {
		proxyForget = "64s"
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	// Draw once immediately, then every 10 s.
	draw := func() {
		st := node.Snapshot()
		view.Draw(tui.PrpdStats{
			Role:         st.Role,
			Name:         cfg.Node.Name,
			PRPID:        node.Config.PRPID,
			NetID:        st.NetID,
			LanID:        st.LanID,
			LanAIn:       st.LanAIn,
			LanAOut:      st.LanAOut,
			LanBIn:       st.LanBIn,
			LanBOut:      st.LanBOut,
			InterIn:      st.InterIn,
			InterOut:     st.InterOut,
			SupSent:      st.SupSent,
			DupTableSize: st.DupTableSize,
			Drops:        st.Drops,
		}, tui.PrpdSettings{
			Interfaces:     interfaces,
			TrailerEnabled: cfg.PRP.TrailerEnabled,
			Supervision:    sup,
			NodeForget:     nodeForget,
			ProxyForget:    proxyForget,
			EntryForget:    entryForget,
			ForwardAll:     cfg.Interlink.ForwardAll,
			VLANFilter:     vlan,
			Multicast:      mc,
			DebugFrames:    os.Getenv("DEBUG_FRAMES") == "1",
		}, ring.Lines(8))
	}
	draw()
	for {
		select {
		case <-ticker.C:
			draw()
		case <-node.StopChan():
			return
		}
	}
}

// formatDrops renders the drop-reason counters as "dup=1 own=2 ...".
func formatDrops(drops map[string]int) string {
	if len(drops) == 0 {
		return "-"
	}
	keys := []string{"dup", "own", "path", "filter", "malformed"}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		if v, ok := drops[k]; ok && v > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", k, v))
		}
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ")
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
