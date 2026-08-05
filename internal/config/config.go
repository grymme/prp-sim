package config

import (
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the YAML configuration for a PRP node. Every field has a
// default; only node.role, interfaces.lan_a and interfaces.lan_b are
// mandatory. Fields that exist in a config file but not in this struct are
// silently ignored (yaml.v3 behaviour), so removing a key is always safe.
type Config struct {
	Node struct {
		Name string `yaml:"name"`
		Role string `yaml:"role"`
	} `yaml:"node"`

	Interfaces struct {
		LanA      string `yaml:"lan_a"`
		LanB      string `yaml:"lan_b"`
		Interlink string `yaml:"interlink"`

		// IPv4 holds optional static IPv4 addresses (CIDR) per port.
		// Empty string = leave the port unnumbered.
		IPv4 struct {
			LanA      string `yaml:"lan_a"`
			LanB      string `yaml:"lan_b"`
			Interlink string `yaml:"interlink"`
		} `yaml:"ipv4"`
	} `yaml:"interfaces"`

	// HSR holds the HSR ring configuration. Only the hsr-prp role uses
	// prp_id (NetId) and lan_id (which PRP LAN the interlink attaches
	// to); hsr-san/hsr-hsr leave them unset.
	HSR struct {
		// PRPID is the NetId of the coupled PRP network, range 1-6.
		PRPID int `yaml:"prp_id"`
		// LanID is the coupled PRP LAN: "A" or "B".
		LanID string `yaml:"lan_id"`
	} `yaml:"hsr"`

	PRP struct {
		PRPID          int  `yaml:"prp_id"`
		TrailerEnabled bool `yaml:"trailer_enabled"`
	} `yaml:"prp"`

	Supervision struct {
		Enabled             bool   `yaml:"enabled"`
		LifeCheckInterval   string `yaml:"life_check_interval"`
		NodeForgetTime      string `yaml:"node_forget_time"`
		ProxyNodeForgetTime string `yaml:"proxy_node_forget_time"`
	} `yaml:"supervision"`

	DuplicateDetection struct {
		EntryForgetTime  string `yaml:"entry_forget_time"`
		MaxNodeTableSize int    `yaml:"max_node_table_size"`
	} `yaml:"duplicate_detection"`

	MulticastFilter struct {
		FirstOctet string `yaml:"first_octet"`
	} `yaml:"multicast_filter"`

	Interlink struct {
		ForwardAll bool  `yaml:"forward_all"`
		VLANFilter []int `yaml:"vlan_filter"`
	} `yaml:"interlink"`
}

// Load reads and validates the configuration from path, applying
// environment-variable overrides on top.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// Normalise the role: `redbox` is a legacy alias for `prp-san`.
	switch c.Node.Role {
	case "redbox":
		c.Node.Role = "prp-san"
	case "prp-san", "hsr-san", "hsr-prp", "hsr-hsr":
	default:
		return nil, fmt.Errorf("invalid role: %s (must be prp-san, hsr-san, hsr-prp or hsr-hsr)", c.Node.Role)
	}

	if c.Interfaces.LanA == "" || c.Interfaces.LanB == "" {
		return nil, fmt.Errorf("lan_a and lan_b interfaces must be specified")
	}

	// hsr-prp couples the HSR ring to one PRP LAN: the interlink must be
	// configured with a valid hsr.prp_id (NetId 1-6) and hsr.lan_id (A|B).
	if c.Node.Role == "hsr-prp" {
		if c.Interfaces.Interlink == "" {
			return nil, fmt.Errorf("hsr-prp role requires interfaces.interlink")
		}
		if c.HSR.LanID != "A" && c.HSR.LanID != "B" {
			return nil, fmt.Errorf("hsr-prp role requires hsr.lan_id: A or B (got %q)", c.HSR.LanID)
		}
		if c.HSR.PRPID < 1 || c.HSR.PRPID > 6 {
			return nil, fmt.Errorf("hsr-prp role requires hsr.prp_id: 1-6 (got %d)", c.HSR.PRPID)
		}
	}

	// Validate the multicast filter pattern early so a typo is reported at
	// load time instead of silently disabling the filter at runtime.
	if c.MulticastFilter.FirstOctet != "" && !validMulticastPattern(c.MulticastFilter.FirstOctet) {
		return nil, fmt.Errorf("invalid multicast_filter.first_octet %q: use hex bytes separated by - or : (e.g. \"01\", \"01-00-5E\", \"33-33\")", c.MulticastFilter.FirstOctet)
	}

	// Validate static port IPs early (IPv4 CIDR only).
	for port, cidr := range map[string]string{
		"interfaces.ipv4.lan_a":     c.Interfaces.IPv4.LanA,
		"interfaces.ipv4.lan_b":     c.Interfaces.IPv4.LanB,
		"interfaces.ipv4.interlink": c.Interfaces.IPv4.Interlink,
	} {
		if cidr == "" {
			continue
		}
		if err := validateCIDR(cidr); err != nil {
			return nil, fmt.Errorf("%s: %w", port, err)
		}
	}

	// Auto-derive unique prp_id from hostname if set to 0 (default).
	// GNS3 assigns a unique hostname to each container (e.g. "prp-sim-1"),
	// so every node in the simulation ends up with a distinct PRP ID.
	if c.PRP.PRPID == 0 {
		hostname, _ := os.Hostname()
		if hostname != "" {
			sum := sha256.Sum256([]byte(hostname))
			// Use lower 16 bits of hash, range 1–65535
			c.PRP.PRPID = (int(sum[0])<<8|int(sum[1]))%65535 + 1
			if c.Node.Name == "" {
				c.Node.Name = hostname
			}
		}
	}

	// Apply environment variable overrides
	if role := os.Getenv("PRP_ROLE"); role != "" {
		switch role {
		case "redbox":
			c.Node.Role = "prp-san"
		case "prp-san", "hsr-san", "hsr-prp", "hsr-hsr":
			c.Node.Role = role
		default:
			return nil, fmt.Errorf("invalid PRP_ROLE: %s (must be prp-san, hsr-san, hsr-prp or hsr-hsr)", role)
		}
	}
	if lanID := os.Getenv("HSR_LAN_ID"); lanID != "" {
		if lanID != "A" && lanID != "B" {
			return nil, fmt.Errorf("invalid HSR_LAN_ID: %s (must be A or B)", lanID)
		}
		c.HSR.LanID = lanID
	}
	if netID := os.Getenv("HSR_PRP_ID"); netID != "" {
		id, err := strconv.Atoi(netID)
		if err != nil || id < 1 || id > 6 {
			return nil, fmt.Errorf("invalid HSR_PRP_ID: %s (must be 1-6)", netID)
		}
		c.HSR.PRPID = id
	}
	if prpID := os.Getenv("PRP_PRP_ID"); prpID != "" {
		if id, err := strconv.Atoi(prpID); err == nil && id > 0 {
			c.PRP.PRPID = id
		}
	}
	// Optional static port IPs via environment variables (precedence over
	// the config file, convenient for per-node GNS3 templates).
	for _, ov := range []struct {
		env  string
		dest *string
	}{
		{"PRP_LAN_A_IP", &c.Interfaces.IPv4.LanA},
		{"PRP_LAN_B_IP", &c.Interfaces.IPv4.LanB},
		{"PRP_INTERLINK_IP", &c.Interfaces.IPv4.Interlink},
	} {
		if v := os.Getenv(ov.env); v != "" {
			if err := validateCIDR(v); err != nil {
				return nil, fmt.Errorf("%s: %w", ov.env, err)
			}
			*ov.dest = v
		}
	}

	return &c, nil
}

// validateCIDR checks that s is a valid IPv4 CIDR (e.g. "10.0.0.1/24").
func validateCIDR(s string) error {
	ip, ipnet, err := net.ParseCIDR(s)
	if err != nil {
		return fmt.Errorf("invalid IPv4 CIDR %q: %v", s, err)
	}
	if ip.To4() == nil {
		return fmt.Errorf("only IPv4 addresses are supported (got %q)", s)
	}
	if ipnet == nil {
		return fmt.Errorf("invalid IPv4 CIDR %q", s)
	}
	return nil
}

// validMulticastPattern reports whether s is a valid multicast filter
// pattern: 1-6 hex bytes, optionally separated by '-' or ':' (e.g. "01",
// "01-00-5E", "33:33", "01005e").
func validMulticastPattern(s string) bool {
	hex := normalizeHex(s)
	if hex == "" || len(hex) > 12 || len(hex)%2 != 0 {
		return false
	}
	for _, r := range hex {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// normalizeHex lowercases s, strips a 0x prefix and '-'/':' separators.
func normalizeHex(s string) string {
	hex := strings.ToLower(s)
	hex = strings.TrimPrefix(hex, "0x")
	hex = strings.ReplaceAll(hex, "-", "")
	hex = strings.ReplaceAll(hex, ":", "")
	return hex
}
