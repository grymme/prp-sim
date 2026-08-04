package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"prp-gns3/internal/config"
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

	// Determine TAP name (default: prp0)
	tapName := cfg.VirtualIface.Name
	if tapName == "" {
		tapName = "prp0"
	}

	// Build PRP configuration
	prpCfg := &prp.Config{
		NodeName:           cfg.Node.Name,
		Role:               cfg.Node.Role,
		LanAInterface:      cfg.Interfaces.LanA,
		LanBInterface:      cfg.Interfaces.LanB,
		InterlinkInterface: cfg.Interfaces.Interlink,
		TapName:            tapName,
		TapMAC:             cfg.VirtualIface.MAC,
		PRPID:              cfg.PRP.PRPID,
		TrailerEnabled:     cfg.PRP.TrailerEnabled,
		Debug:              os.Getenv("DEBUG_FRAMES") == "1",
	}

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
