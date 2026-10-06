// Package scanner wraps standard discovery tools (nmap, DNS, ARP) used to build
// an authorized asset inventory of the operator's own network.
package scanner

import (
	"context"
	"encoding/xml"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Host is the normalized result of scanning a single address.
type Host struct {
	IP       string
	Hostname string
	Status   string
	MAC      string
	Vendor   string
	OSGuess  string
	Ports    []Port
}

type Port struct {
	Port     int
	Protocol string
	State    string
	Service  string
	Product  string
	Version  string
}

// nmap XML schema (subset we consume).
type nmapRun struct {
	Hosts []struct {
		Status struct {
			State string `xml:"state,attr"`
		} `xml:"status"`
		Addresses []struct {
			Addr   string `xml:"addr,attr"`
			Type   string `xml:"addrtype,attr"`
			Vendor string `xml:"vendor,attr"`
		} `xml:"address"`
		Hostnames struct {
			Hostname []struct {
				Name string `xml:"name,attr"`
			} `xml:"hostname"`
		} `xml:"hostnames"`
		Ports struct {
			Port []struct {
				Protocol string `xml:"protocol,attr"`
				PortID   int    `xml:"portid,attr"`
				State    struct {
					State string `xml:"state,attr"`
				} `xml:"state"`
				Service struct {
					Name    string `xml:"name,attr"`
					Product string `xml:"product,attr"`
					Version string `xml:"version,attr"`
				} `xml:"service"`
			} `xml:"port"`
		} `xml:"ports"`
		OS struct {
			OSMatch []struct {
				Name     string `xml:"name,attr"`
				Accuracy int    `xml:"accuracy,attr"`
			} `xml:"osmatch"`
		} `xml:"os"`
	} `xml:"host"`
}

// RunNmap scans the given targets with nmap and returns normalized hosts. The
// flags default to a top-100-port TCP service scan and can be overridden for
// the deployment environment (e.g. unprivileged connect scans).
func RunNmap(ctx context.Context, binary string, flags []string, targets []string) ([]Host, error) {
	if binary == "" {
		binary = "nmap"
	}
	args := append([]string{}, flags...)
	args = append(args, "-oX", "-") // XML to stdout
	args = append(args, targets...)

	cmd := exec.CommandContext(ctx, binary, args...)
	out, err := cmd.Output()
	if err != nil {
		if len(out) == 0 {
			return nil, fmt.Errorf("nmap: %w", err)
		}
		// nmap can exit non-zero yet still emit usable XML; try to parse.
	}

	var run nmapRun
	if err := xml.Unmarshal(out, &run); err != nil {
		return nil, fmt.Errorf("parse nmap xml: %w", err)
	}

	hosts := make([]Host, 0, len(run.Hosts))
	for _, h := range run.Hosts {
		host := Host{Status: h.Status.State}
		for _, a := range h.Addresses {
			switch a.Type {
			case "ipv4", "ipv6":
				host.IP = a.Addr
			case "mac":
				host.MAC = a.Addr
				host.Vendor = a.Vendor
			}
		}
		if len(h.Hostnames.Hostname) > 0 {
			host.Hostname = h.Hostnames.Hostname[0].Name
		}
		if len(h.OS.OSMatch) > 0 {
			host.OSGuess = h.OS.OSMatch[0].Name
		}
		for _, p := range h.Ports.Port {
			if p.State.State != "open" {
				continue
			}
			host.Ports = append(host.Ports, Port{
				Port:     p.PortID,
				Protocol: p.Protocol,
				State:    p.State.State,
				Service:  p.Service.Name,
				Product:  p.Service.Product,
				Version:  p.Service.Version,
			})
		}
		if host.IP == "" {
			continue
		}
		hosts = append(hosts, host)
	}
	return hosts, nil
}

// DefaultFlags builds the nmap argument list from a flag string, falling back to
// a safe default service scan of the top 100 TCP ports.
func DefaultFlags(override string) []string {
	if strings.TrimSpace(override) != "" {
		return strings.Fields(override)
	}
	return []string{"-sT", "-sV", "--top-ports", "100", "--open", "-T4", "-n"}
}

// Discover runs a fast host-discovery (ping) sweep over the targets and returns
// the IPs that are up. Scanning only live hosts afterward is what makes large
// ranges (e.g. 10.0.0.0/8, 172.16.0.0/12) tractable.
func Discover(ctx context.Context, binary string, flags []string, targets []string) ([]string, error) {
	if binary == "" {
		binary = "nmap"
	}
	args := append([]string{}, flags...)
	args = append(args, "-oX", "-")
	args = append(args, targets...)

	cmd := exec.CommandContext(ctx, binary, args...)
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("nmap discovery: %w", err)
	}
	var run nmapRun
	if err := xml.Unmarshal(out, &run); err != nil {
		return nil, fmt.Errorf("parse discovery xml: %w", err)
	}
	var live []string
	for _, h := range run.Hosts {
		if h.Status.State != "up" {
			continue
		}
		for _, a := range h.Addresses {
			if a.Type == "ipv4" || a.Type == "ipv6" {
				live = append(live, a.Addr)
			}
		}
	}
	return live, nil
}

// DiscoveryFlags returns the nmap flags for the ping-sweep phase.
func DiscoveryFlags(override string) []string {
	if strings.TrimSpace(override) != "" {
		return strings.Fields(override)
	}
	return []string{"-sn", "-n", "-T4", "--min-rate", "1000"}
}

// TopPortsNote documents the ports covered by --top-ports 100 (informational).
func TopPortsNote() string {
	common := []int{20, 21, 22, 23, 25, 53, 80, 110, 111, 135, 137, 138, 139, 143,
		443, 445, 993, 995, 1433, 1723, 3306, 3389, 5900, 8080, 8443}
	s := make([]string, len(common))
	for i, p := range common {
		s[i] = strconv.Itoa(p)
	}
	return strings.Join(s, ",")
}
