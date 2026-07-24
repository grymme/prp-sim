package config

import (
	"crypto/sha256"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Node struct {
		Name string `yaml:"name"`
		Role string `yaml:"role"`
	} `yaml:"node"`

	Interfaces struct {
		LanA      string `yaml:"lan_a"`
		LanB      string `yaml:"lan_b"`
		Interlink string `yaml:"interlink"`
	} `yaml:"interfaces"`

	VirtualIface struct {
		Name string `yaml:"name"`
		MAC  string `yaml:"mac"`
	} `yaml:"virtual_iface"`

	PRP struct {
		PRPID          int    `yaml:"prp_id"`
		LANID          string `yaml:"lan_id"`
		Suffix         string `yaml:"suffix"`
		TrailerEnabled bool   `yaml:"trailer_enabled"`
	} `yaml:"prp"`

	Supervision struct {
		Enabled              bool   `yaml:"enabled"`
		LifeCheckInterval    string `yaml:"life_check_interval"`
		NodeForgetTime       string `yaml:"node_forget_time"`
		ProxyNodeForgetTime  string `yaml:"proxy_node_forget_time"`
		NodeRebootInterval   string `yaml:"node_reboot_interval"`
	} `yaml:"supervision"`

	DuplicateDetection struct {
		EntryForgetTime  string `yaml:"entry_forget_time"`
		MaxNodeTableSize int    `yaml:"max_node_table_size"`
	} `yaml:"duplicate_detection"`

	MulticastFilter struct {
		FirstOctet string `yaml:"first_octet"`
	} `yaml:"multicast_filter"`

	Interlink struct {
		Mode      string   `yaml:"mode"`
		ForwardAll bool    `yaml:"forward_all"`
		VLANFilter []int   `yaml:"vlan_filter"`
	} `yaml:"interlink"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if c.Node.Role != "redbox" && c.Node.Role != "dan" {
		return nil, fmt.Errorf("invalid role: %s (must be redbox or dan)", c.Node.Role)
	}

	if c.Interfaces.LanA == "" || c.Interfaces.LanB == "" {
		return nil, fmt.Errorf("lan_a and lan_b interfaces must be specified")
	}

	// Auto-derive unique prp_id from hostname if set to 0 (default).
	// GNS3 assigns a unique hostname to each container (e.g. "prp-sim-1").
	if c.PRP.PRPID == 0 {
		hostname, _ := os.Hostname()
		if hostname != "" {
			sum := sha256.Sum256([]byte(hostname))
			// Use lower 16 bits of hash, range 1–65535
			c.PRP.PRPID = (int(sum[0])<<8 | int(sum[1]))%65535 + 1
			if c.Node.Name == "" {
				c.Node.Name = hostname
			}
		}
	}

	return &c, nil
}
