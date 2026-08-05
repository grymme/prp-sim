package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	_, err := Load("nonexistent.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadValid(t *testing.T) {
	cfg, err := Load("../../config.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Node.Role == "" {
		t.Fatal("expected role to be set")
	}
}

// writeTempConfig writes a YAML config to a temp file and returns its path.
func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

func TestLoadStaticIPs(t *testing.T) {
	path := writeTempConfig(t, `
node:
  role: redbox
interfaces:
  lan_a: eth0
  lan_b: eth1
  interlink: eth2
  ipv4:
    lan_a: "10.0.0.1/24"
    interlink: "192.168.1.5/24"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Interfaces.IPv4.LanA != "10.0.0.1/24" {
		t.Errorf("lan_a IP: got %q", cfg.Interfaces.IPv4.LanA)
	}
	if cfg.Interfaces.IPv4.LanB != "" {
		t.Errorf("lan_b IP should stay empty, got %q", cfg.Interfaces.IPv4.LanB)
	}
	if cfg.Interfaces.IPv4.Interlink != "192.168.1.5/24" {
		t.Errorf("interlink IP: got %q", cfg.Interfaces.IPv4.Interlink)
	}
}

func TestLoadInvalidIP(t *testing.T) {
	path := writeTempConfig(t, `
node:
  role: redbox
interfaces:
  lan_a: eth0
  lan_b: eth1
  ipv4:
    lan_a: "10.0.0.1"        # missing prefix
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for non-CIDR IP")
	}

	path = writeTempConfig(t, `
node:
  role: redbox
interfaces:
  lan_a: eth0
  lan_b: eth1
  ipv4:
    lan_a: "fe80::1/64"       # IPv6 not supported
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for IPv6 address")
	}
}

func TestLoadInvalidMulticastPattern(t *testing.T) {
	path := writeTempConfig(t, `
node:
  role: redbox
interfaces:
  lan_a: eth0
  lan_b: eth1
multicast_filter:
  first_octet: "not-hex!"
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for invalid multicast pattern")
	}
}

func TestEnvIPOverrides(t *testing.T) {
	t.Setenv("PRP_LAN_A_IP", "10.1.0.10/24")
	t.Setenv("PRP_LAN_B_IP", "10.1.0.11/24")
	defer os.Unsetenv("PRP_LAN_A_IP")
	defer os.Unsetenv("PRP_LAN_B_IP")

	path := writeTempConfig(t, `
node:
  role: redbox
interfaces:
  lan_a: eth0
  lan_b: eth1
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Interfaces.IPv4.LanA != "10.1.0.10/24" || cfg.Interfaces.IPv4.LanB != "10.1.0.11/24" {
		t.Errorf("env overrides not applied: %+v", cfg.Interfaces.IPv4)
	}
}

func TestEnvInvalidIP(t *testing.T) {
	t.Setenv("PRP_LAN_A_IP", "300.1.1.1/24")
	defer os.Unsetenv("PRP_LAN_A_IP")

	path := writeTempConfig(t, `
node:
  role: redbox
interfaces:
  lan_a: eth0
  lan_b: eth1
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for invalid PRP_LAN_A_IP")
	}
}

func TestHSRPRPRoleRequiresLanID(t *testing.T) {
	path := writeTempConfig(t, `
node:
  role: hsr-prp
interfaces:
  lan_a: eth0
  lan_b: eth1
  interlink: eth2
hsr:
  prp_id: 1
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error: hsr-prp without hsr.lan_id")
	}
}

func TestHSRPRPRoleRequiresNetID(t *testing.T) {
	path := writeTempConfig(t, `
node:
  role: hsr-prp
interfaces:
  lan_a: eth0
  lan_b: eth1
  interlink: eth2
hsr:
  lan_id: A
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error: hsr-prp without hsr.prp_id")
	}
}

func TestHSRPRPRoleNetIDRange(t *testing.T) {
	path := writeTempConfig(t, `
node:
  role: hsr-prp
interfaces:
  lan_a: eth0
  lan_b: eth1
  interlink: eth2
hsr:
  prp_id: 9
  lan_id: A
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error: NetId 9 out of range")
	}
}

func TestHSRBlockValid(t *testing.T) {
	path := writeTempConfig(t, `
node:
  role: hsr-prp
interfaces:
  lan_a: eth0
  lan_b: eth1
  interlink: eth2
hsr:
  prp_id: 3
  lan_id: B
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HSR.PRPID != 3 || cfg.HSR.LanID != "B" {
		t.Errorf("hsr block parsed as prp_id=%d lan_id=%s, want 3/B", cfg.HSR.PRPID, cfg.HSR.LanID)
	}
}

func TestHSREnvOverrides(t *testing.T) {
	t.Setenv("HSR_PRP_ID", "5")
	t.Setenv("HSR_LAN_ID", "A")
	path := writeTempConfig(t, `
node:
  role: hsr-prp
interfaces:
  lan_a: eth0
  lan_b: eth1
  interlink: eth2
hsr:
  prp_id: 1
  lan_id: B
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HSR.PRPID != 5 || cfg.HSR.LanID != "A" {
		t.Errorf("env overrides not applied: prp_id=%d lan_id=%s, want 5/A", cfg.HSR.PRPID, cfg.HSR.LanID)
	}
}

func TestHSRHSRRoleAccepted(t *testing.T) {
	path := writeTempConfig(t, `
node:
  role: hsr-hsr
interfaces:
  lan_a: eth0
  lan_b: eth1
  interlink: eth2
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Node.Role != "hsr-hsr" {
		t.Errorf("role = %s, want hsr-hsr", cfg.Node.Role)
	}
}
