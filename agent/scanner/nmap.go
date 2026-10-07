// Package scanner wraps standard discovery tools (nmap, DNS, ARP) used to build
// an authorized asset inventory of the operator's own network.
package scanner

import (
	"bytes"
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

// nmapHost is one <host> element of nmap's XML output (subset we consume).
type nmapHost struct {
	Timedout bool `xml:"timedout,attr"`
	Status   struct {
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
}

type nmapRun struct {
	Hosts []nmapHost `xml:"host"`
}

// toHost normalizes an nmap host, keeping only open ports.
func (h nmapHost) toHost() Host {
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
	return host
}

// RunNmap scans the given targets with nmap and returns normalized hosts. Hosts
// that hit --host-timeout are dropped: their results are incomplete and must not
// overwrite what the inventory already knows about them.
func RunNmap(ctx context.Context, binary string, flags []string, targets []string) ([]Host, error) {
	if binary == "" {
		binary = "nmap"
	}
	args := append([]string{}, flags...)
	args = append(args, "-oX", "-") // XML to stdout
	args = append(args, targets...)

	cmd := exec.CommandContext(ctx, binary, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if len(out) == 0 {
			return nil, fmt.Errorf("nmap: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		// nmap can exit non-zero yet still emit usable XML; try to parse.
	}

	var run nmapRun
	if err := xml.Unmarshal(out, &run); err != nil {
		return nil, fmt.Errorf("parse nmap xml: %w", err)
	}

	hosts := make([]Host, 0, len(run.Hosts))
	for _, h := range run.Hosts {
		if h.Timedout {
			continue
		}
		host := h.toHost()
		if host.IP == "" {
			continue
		}
		hosts = append(hosts, host)
	}
	return hosts, nil
}

// PortFlags returns the nmap flags for phase 3 (open ports), defaulting to a
// connect scan of the top 100 TCP ports.
func PortFlags(override string) []string {
	if strings.TrimSpace(override) != "" {
		return strings.Fields(override)
	}
	return []string{"-sT", "--top-ports", "100", "--open", "-T4", "-n"}
}

// DetectFlags returns the nmap flags for phase 4: OS detection plus service
// version detection (the platforms published on the open ports).
func DetectFlags(override string) []string {
	if strings.TrimSpace(override) != "" {
		return strings.Fields(override)
	}
	return []string{"-sV", "-O", "--osscan-guess", "-T4", "-n"}
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
