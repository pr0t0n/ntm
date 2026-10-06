package api

import (
	"sort"
	"strings"

	"github.com/valid/ntm/platform/internal/models"
)

// classifyAsset derives an asset_type (computer | server | network | printer |
// unknown) from the open-port fingerprint and OS hints. This is heuristic and
// mirrors how asset-inventory tools categorise discovered hosts.
func classifyAsset(h models.ReportedHost) string {
	ports := map[int]bool{}
	services := map[string]bool{}
	for _, p := range h.Ports {
		if p.State == "open" {
			ports[p.Port] = true
			if p.Service != "" {
				services[strings.ToLower(p.Service)] = true
			}
		}
	}
	os := strings.ToLower(h.OSGuess + " " + h.Vendor)

	// Network equipment: SNMP, telnet on infra vendors, or vendor names.
	if ports[161] || ports[623] ||
		strings.Contains(os, "cisco") || strings.Contains(os, "juniper") ||
		strings.Contains(os, "mikrotik") || strings.Contains(os, "ubiquiti") ||
		strings.Contains(os, "fortinet") || strings.Contains(os, "aruba") {
		return "network"
	}
	// Printers.
	if ports[9100] || ports[515] || ports[631] || services["ipp"] || services["jetdirect"] {
		return "printer"
	}
	// Servers: expose server-class services.
	serverPorts := []int{80, 443, 8080, 8443, 3306, 5432, 1433, 25, 53, 389, 636, 3389, 21, 25, 993, 995, 5000, 6379, 27017}
	serverHits := 0
	for _, p := range serverPorts {
		if ports[p] {
			serverHits++
		}
	}
	if services["http"] || services["https"] || services["mysql"] ||
		services["postgresql"] || services["ms-sql-s"] || services["domain"] ||
		services["ldap"] || serverHits >= 2 {
		return "server"
	}
	// Workstations: SMB/RDP/SSH only, or Windows/Linux desktop hints.
	if ports[445] || ports[139] || ports[3389] || ports[22] ||
		strings.Contains(os, "windows") || strings.Contains(os, "linux") ||
		strings.Contains(os, "mac") {
		return "computer"
	}
	if len(ports) > 0 {
		return "computer"
	}
	return "unknown"
}

// normalizeOS collapses a raw nmap OS string into a coarse family for the
// dashboard breakdown.
func normalizeOS(raw string) string {
	s := strings.ToLower(raw)
	switch {
	case s == "":
		return ""
	case strings.Contains(s, "windows"):
		return "Windows"
	case strings.Contains(s, "ubuntu"):
		return "Linux (Ubuntu)"
	case strings.Contains(s, "debian"):
		return "Linux (Debian)"
	case strings.Contains(s, "centos") || strings.Contains(s, "red hat") || strings.Contains(s, "rhel"):
		return "Linux (RHEL)"
	case strings.Contains(s, "linux"):
		return "Linux"
	case strings.Contains(s, "mac") || strings.Contains(s, "darwin") || strings.Contains(s, "apple"):
		return "macOS"
	case strings.Contains(s, "cisco") || strings.Contains(s, "ios"):
		return "Network OS"
	default:
		return strings.Title(raw)
	}
}

// topServices is a small helper used by tests/debug to summarise applications.
func topServices(hosts []models.Host) []models.AppCount {
	counts := map[string]int{}
	for _, h := range hosts {
		for _, p := range h.Ports {
			name := p.Service
			if name == "" {
				name = "unknown"
			}
			counts[name]++
		}
	}
	out := make([]models.AppCount, 0, len(counts))
	for k, v := range counts {
		out = append(out, models.AppCount{Name: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}
