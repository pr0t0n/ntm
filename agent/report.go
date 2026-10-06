package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/valid/ntm/agent/scanner"
)

// Wire types mirror the platform's ingest payload.
type reportedPort struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	State    string `json:"state"`
	Service  string `json:"service"`
	Product  string `json:"product"`
	Version  string `json:"version"`
}

type reportedHost struct {
	IP       string         `json:"ip"`
	Hostname string         `json:"hostname"`
	Status   string         `json:"status"`
	MAC      string         `json:"mac"`
	Vendor   string         `json:"vendor"`
	OSGuess  string         `json:"os_guess"`
	Ports    []reportedPort `json:"ports"`
}

type dnsRecord struct {
	HostIP string `json:"host_ip"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Value  string `json:"value"`
}

type scanReport struct {
	Hosts []reportedHost `json:"hosts"`
	DNS   []dnsRecord    `json:"dns"`
}

func buildReport(hosts []scanner.Host, dns []scanner.DNSRecord) scanReport {
	rep := scanReport{}
	for _, h := range hosts {
		rh := reportedHost{
			IP:       h.IP,
			Hostname: h.Hostname,
			Status:   orDefault(h.Status, "up"),
			MAC:      h.MAC,
			Vendor:   h.Vendor,
			OSGuess:  h.OSGuess,
		}
		for _, p := range h.Ports {
			rh.Ports = append(rh.Ports, reportedPort{
				Port: p.Port, Protocol: p.Protocol, State: p.State,
				Service: p.Service, Product: p.Product, Version: p.Version,
			})
		}
		rep.Hosts = append(rep.Hosts, rh)
	}
	for _, d := range dns {
		rep.DNS = append(rep.DNS, dnsRecord{HostIP: d.HostIP, Name: d.Name, Type: d.Type, Value: d.Value})
	}
	return rep
}

func uploadReport(client *http.Client, cfg agentConfig, state agentState, rep scanReport) error {
	body, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, cfg.PlatformURL+"/api/v1/scan-results", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+state.AgentToken)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errStatus(resp)
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

// --- state persistence ---

func readState(path string) (agentState, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return agentState{}, false
	}
	var st agentState
	if err := json.Unmarshal(data, &st); err != nil || st.AgentToken == "" {
		return agentState{}, false
	}
	return st, true
}

func writeState(path string, st agentState) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return
	}
	data, _ := json.Marshal(st)
	_ = os.WriteFile(path, data, 0o600)
}

// --- small helpers ---

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func hostnameOr(def string) string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return def
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func errStatus(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("platform returned %s: %s", resp.Status, strings.TrimSpace(string(b)))
}
