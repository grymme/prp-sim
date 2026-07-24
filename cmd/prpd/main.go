package main

import (
	"fmt"
	"os"

	"prp-gns3/internal/config"
	"prp-gns3/internal/supervision"
	"prp-gns3/internal/tap"
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

	tap.Create(cfg.VirtualIface.Name)
	bindRaw(cfg.Interfaces.LanA)
	bindRaw(cfg.Interfaces.LanB)
	supervision.SendInterval(cfg.Interfaces.LanA, 2)

	fmt.Println("loop: active")
}

func bindRaw(iface string) {
	fmt.Printf("raw: bound %s\n", iface)
}
