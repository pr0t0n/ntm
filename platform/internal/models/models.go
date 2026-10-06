package models

import "time"

// Agent is a scanning collector enrolled into the platform. Each agent runs on
// an authorized host inside the customer network and reports asset data back
// over HTTPS/TLS.
type Agent struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	AgentToken string    `json:"-"` // bearer token used by the agent to authenticate ingests
	Ranges     []string  `json:"ranges"`
	LastSeen   time.Time `json:"last_seen"`
	CreatedAt  time.Time `json:"created_at"`
}

// Host is a discovered asset in the inventory (CMDB record).
type Host struct {
	ID        int64     `json:"id"`
	IP        string    `json:"ip"`
	Hostname  string    `json:"hostname"`
	Status    string    `json:"status"` // up | down
	MAC       string    `json:"mac"`
	Vendor    string    `json:"vendor"`
	OSGuess   string    `json:"os_guess"`
	AssetType string    `json:"asset_type"` // computer | server | network | printer | unknown
	AgentID   int64     `json:"agent_id"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Ports     []Port    `json:"ports,omitempty"`
}

// Port is an open TCP port observed on a host, with service fingerprint data.
type Port struct {
	ID       int64  `json:"id"`
	HostID   int64  `json:"host_id"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	State    string `json:"state"`
	Service  string `json:"service"`
	Product  string `json:"product"`
	Version  string `json:"version"`
}

// DNSRecord captures a name/value pair found during DNS enumeration.
type DNSRecord struct {
	ID     int64  `json:"id"`
	HostIP string `json:"host_ip"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Value  string `json:"value"`
}

// --- Wire payloads exchanged between agent and platform ---

// EnrollRequest is sent by an agent during installation using the installation
// token to obtain its own long-lived agent token.
type EnrollRequest struct {
	InstallToken string   `json:"install_token"`
	Name         string   `json:"name"`
	Ranges       []string `json:"ranges"`
}

type EnrollResponse struct {
	AgentID    int64  `json:"agent_id"`
	AgentToken string `json:"agent_token"`
}

// ScanReport is the batch of findings an agent uploads after a scan run.
type ScanReport struct {
	Hosts []ReportedHost `json:"hosts"`
	DNS   []DNSRecord    `json:"dns"`
}

type ReportedHost struct {
	IP       string         `json:"ip"`
	Hostname string         `json:"hostname"`
	Status   string         `json:"status"`
	MAC      string         `json:"mac"`
	Vendor   string         `json:"vendor"`
	OSGuess  string         `json:"os_guess"`
	Ports    []ReportedPort `json:"ports"`
}

type ReportedPort struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	State    string `json:"state"`
	Service  string `json:"service"`
	Product  string `json:"product"`
	Version  string `json:"version"`
}

// --- Dashboard aggregates ---

type DashboardStats struct {
	TotalHosts  int              `json:"total_hosts"`
	HostsUp     int              `json:"hosts_up"`
	HostsDown   int              `json:"hosts_down"`
	ByAssetType map[string]int   `json:"by_asset_type"`
	ByOS        map[string]int   `json:"by_os"`
	TopApps     []AppCount       `json:"top_apps"`
	OpenPorts   int              `json:"open_ports"`
	Agents      int              `json:"agents"`
	Recent      []Host           `json:"recent"`
}

type AppCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}
