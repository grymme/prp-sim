// internal/prp/role.go — single source of truth for RedBox role semantics.
//
// A RedBox has two redundancy ports (A/B) and one interlink. The protocol
// on the redundancy ports and the semantics of the interlink depend on the
// configured role:
//
//	prp-san  — redundancy ports speak PRP (two LANs); interlink is a SAN.
//	           (legacy name: "redbox")
//	hsr-san  — redundancy ports speak HSR (one ring); interlink is a SAN.
//	hsr-prp  — redundancy ports speak HSR (one ring); interlink attaches
//	           to ONE PRP LAN (A or B) of a coupled PRP network (NetId).
package prp

import "fmt"

// Role is the node role as configured (normalised by the config loader).
type Role string

const (
	RolePRPSan Role = "prp-san" // SAN into two PRP LANs (legacy "redbox")
	RoleHSRSan Role = "hsr-san" // SAN into an HSR ring
	RoleHSRPRP Role = "hsr-prp" // HSR ring into one PRP LAN (coupling)
	RoleHSRHSR Role = "hsr-hsr" // two HSR rings coupled (QuadBox)
)

// ParseRole normalises a role string; "redbox" maps to RolePRPSan.
func ParseRole(s string) (Role, error) {
	switch s {
	case "", "redbox":
		return RolePRPSan, nil
	case "prp-san":
		return RolePRPSan, nil
	case "hsr-san":
		return RoleHSRSan, nil
	case "hsr-prp":
		return RoleHSRPRP, nil
	case "hsr-hsr":
		return RoleHSRHSR, nil
	}
	return "", fmt.Errorf("invalid role %q", s)
}

// RoleInfo describes the port semantics for a role.
type RoleInfo struct {
	Role Role

	// RedundancyPortsAreHSR is true when the A/B ports carry an HSR ring
	// (hsr-san, hsr-prp); false for PRP LANs (prp-san).
	RedundancyPortsAreHSR bool
	// CoupledToPRP is true when the interlink speaks PRP RCT (hsr-prp).
	CoupledToPRP bool
	// InterlinkIsSAN is true when the interlink is a plain Ethernet SAN
	// port (prp-san, hsr-san).
	InterlinkIsSAN bool
	// InterlinkIsHSR is true when the interlink carries HSR-tagged
	// traffic to another HSR ring (hsr-hsr, QuadBox).
	InterlinkIsHSR bool
}

func infoFor(r Role) RoleInfo {
	switch r {
	case RoleHSRSan:
		return RoleInfo{Role: r, RedundancyPortsAreHSR: true, InterlinkIsSAN: true}
	case RoleHSRPRP:
		return RoleInfo{Role: r, RedundancyPortsAreHSR: true, CoupledToPRP: true}
	case RoleHSRHSR:
		// QuadBox: ring ports are HSR; the interlink connects to another
		// HSR ring (forwarded as HSR-tagged traffic).
		return RoleInfo{Role: r, RedundancyPortsAreHSR: true, InterlinkIsHSR: true}
	default: // prp-san
		return RoleInfo{Role: RolePRPSan, InterlinkIsSAN: true}
	}
}

// Info returns the port semantics for the node's role. An unknown role
// falls back to prp-san (config validation should have rejected it).
func (n *Node) roleInfo() RoleInfo {
	r, err := ParseRole(n.Config.Role)
	if err != nil {
		return infoFor(RolePRPSan)
	}
	return infoFor(r)
}

// IsHSRNode reports whether the redundancy ports carry an HSR ring.
func (n *Node) IsHSRNode() bool { return n.roleInfo().RedundancyPortsAreHSR }

// IsHSRPRPCoupling reports whether the interlink couples to a PRP LAN.
func (n *Node) IsHSRPRPCoupling() bool { return n.roleInfo().CoupledToPRP }

// IsHSRHSR reports whether the node couples two HSR rings (QuadBox).
func (n *Node) IsHSRHSR() bool { return n.roleInfo().InterlinkIsHSR }

// LanID returns the configured PRP LAN id for the interlink (hsr-prp);
// 0 for LAN A, 1 for LAN B. Falls back to LAN A for non-coupling roles.
func (n *Node) LanID() int {
	if n.IsHSRPRPCoupling() && n.Config.LanID == "B" {
		return 1
	}
	return 0
}

// NetID returns the configured PRP NetId (hsr-prp); 1 if unset.
func (n *Node) NetID() int {
	if n.IsHSRPRPCoupling() && n.Config.NetID > 0 {
		return n.Config.NetID
	}
	return 1
}
