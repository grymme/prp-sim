package prp

import "testing"

func TestParseRole(t *testing.T) {
	cases := []struct {
		in   string
		want Role
		ok   bool
	}{
		{"", RolePRPSan, true},
		{"redbox", RolePRPSan, true},
		{"prp-san", RolePRPSan, true},
		{"hsr-san", RoleHSRSan, true},
		{"hsr-prp", RoleHSRPRP, true},
		{"dan", "", false},
		{"bogus", "", false},
	}
	for _, c := range cases {
		got, err := ParseRole(c.in)
		if c.ok && err != nil {
			t.Errorf("ParseRole(%q): unexpected error %v", c.in, err)
			continue
		}
		if !c.ok && err == nil {
			t.Errorf("ParseRole(%q): expected error, got %q", c.in, got)
			continue
		}
		if c.ok && got != c.want {
			t.Errorf("ParseRole(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRoleInfo(t *testing.T) {
	cases := []struct {
		role    Role
		hsr     bool
		coupled bool
		san     bool
	}{
		{RolePRPSan, false, false, true},
		{RoleHSRSan, true, false, true},
		{RoleHSRPRP, true, true, false},
	}
	for _, c := range cases {
		n := NewNode(&Config{Role: string(c.role)})
		if n.IsHSRNode() != c.hsr {
			t.Errorf("role %s: IsHSRNode=%v want %v", c.role, n.IsHSRNode(), c.hsr)
		}
		if n.IsHSRPRPCoupling() != c.coupled {
			t.Errorf("role %s: IsHSRPRPCoupling=%v want %v", c.role, n.IsHSRPRPCoupling(), c.coupled)
		}
	}
}

func TestLanIDNetID(t *testing.T) {
	n := NewNode(&Config{Role: "hsr-prp", LanID: "B", NetID: 3})
	if n.LanID() != 1 {
		t.Errorf("LanID() = %d, want 1 (B)", n.LanID())
	}
	if n.NetID() != 3 {
		t.Errorf("NetID() = %d, want 3", n.NetID())
	}
	// Non-coupling roles fall back sanely.
	n2 := NewNode(&Config{Role: "hsr-san"})
	if n2.LanID() != 0 || n2.NetID() != 1 {
		t.Errorf("fallback LanID/NetID = %d/%d, want 0/1", n2.LanID(), n2.NetID())
	}
}
