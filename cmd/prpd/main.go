package main

import (
	"fmt"
	"os"

	"prp-gns3/internal/config"
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

	fmt.Printf("prpd: role=%s name=%s\n", cfg.Node.Role, cfg.Node.Name)
}
func bindRaw(iface string) {
	fmt.Printf("raw: bound %s\n", iface)
}

func init() {
	bindRaw("eth0")
	bindRaw("eth1")
}
