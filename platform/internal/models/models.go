package models

import "time"

// Agent is a scanning collector enrolled into the platform. Each agent runs on
// an authorized host inside the customer network and reports asset data back
// over HTTPS/TLS.
type Agent struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	AgentToken    string    `json:"-"` // bearer token used by the agent to authenticate ingests
	Ranges        []string  `json:"ranges"`
	LastSeen      time.Time `json:"last_seen"`
	LastScanAt    time.Time `json:"last_scan_at"`
	ScanRequested bool      `json:"scan_requested"`
	ScanState     string    `json:"scan_state"` // idle | running
	ScanStartedAt time.Time `json:"scan_started_at"`
	// Phased discovery progress: ping | hostname | ports | detect ('' when idle),
	// when the current phase began, and how many live hosts the scan found.
	ScanPhase      string    `json:"scan_phase"`
	PhaseStartedAt time.Time `json:"phase_started_at"`
	ScanHosts      int       `json:"scan_hosts"`
	IP             string    `json:"ip"`              // agent's own primary IP
	SuggestedRange string    `json:"suggested_range"` // /24 derived from IP (UI hint)
	ScanRange      string    `json:"scan_range"`      // range chosen for the next/last run
	Online        bool      `json:"online"`
	CreatedAt     time.Time `json:"created_at"`
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
	IP           string   `json:"ip"`
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

// PhaseReport is the batch of results an agent uploads during one discovery
// phase: ping (live hosts) | hostname | ports | detect (OS + published services).
type PhaseReport struct {
	Phase string         `json:"phase"`
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

// ResetResult reports how many inventory records a reset removed.
type ResetResult struct {
	Hosts int `json:"hosts"`
	Ports int `json:"ports"`
	DNS   int `json:"dns"`
}
