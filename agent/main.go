package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/valid/ntm/agent/scanner"
)

// agentConfig is sourced from environment variables set at install time.
type agentConfig struct {
	PlatformURL  string // e.g. https://ntm-platform:8443
	InstallToken string // pasted by the operator during installation
	AgentName    string
	Ranges       []string
	NmapBinary   string
	NmapFlags    string
	Resolver     string
	Interval     time.Duration
	PollInterval time.Duration
	Insecure     bool // accept self-signed platform cert (lab default)
	StatePath    string
}

type agentState struct {
	AgentID    int64  `json:"agent_id"`
	AgentToken string `json:"agent_token"`
}

func loadConfig() agentConfig {
	ranges := env("NTM_RANGES", "10.0.0.0/8,172.16.0.0/12,192.168.0.0/16")
	interval, err := time.ParseDuration(env("NTM_SCAN_INTERVAL", "30m"))
	if err != nil {
		interval = 30 * time.Minute
	}
	poll, err := time.ParseDuration(env("NTM_POLL_INTERVAL", "15s"))
	if err != nil {
		poll = 15 * time.Second
	}
	return agentConfig{
		PlatformURL:  strings.TrimRight(env("NTM_PLATFORM_URL", "https://localhost:8443"), "/"),
		InstallToken: env("NTM_INSTALL_TOKEN", ""),
		AgentName:    env("NTM_AGENT_NAME", hostnameOr("ntm-agent")),
		Ranges:       splitCSV(ranges),
		NmapBinary:   env("NTM_NMAP_BIN", "nmap"),
		NmapFlags:    env("NTM_NMAP_FLAGS", ""),
		Resolver:     env("NTM_DNS_RESOLVER", ""),
		Interval:     interval,
		PollInterval: poll,
		Insecure:     env("NTM_TLS_INSECURE", "true") == "true",
		StatePath:    env("NTM_STATE_PATH", "/var/lib/ntm/agent-state.json"),
	}
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[ntm-agent] ")
	cfg := loadConfig()

	client := &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.Insecure}, //nolint:gosec // lab self-signed cert
		},
	}

	var state agentState
	for attempt := 1; ; attempt++ {
		var err error
		state, err = ensureEnrolled(client, cfg)
		if err == nil {
			break
		}
		log.Printf("enrollment attempt %d failed: %v (retrying in 5s)", attempt, err)
		time.Sleep(5 * time.Second)
	}
	log.Printf("enrolled as agent #%d, scanning ranges: %s", state.AgentID, strings.Join(cfg.Ranges, " "))

	// Run once on start, then combine a periodic scan with on-demand runs
	// triggered from the UI (polled via /agent/commands).
	runScan(client, cfg, state)
	lastFull := time.Now()
	log.Printf("polling for on-demand runs every %s; periodic scan every %s", cfg.PollInterval, cfg.Interval)

	for {
		time.Sleep(cfg.PollInterval)
		if pollCommand(client, cfg, state) {
			log.Printf("on-demand scan requested from platform")
			runScan(client, cfg, state)
			lastFull = time.Now()
			continue
		}
		if time.Since(lastFull) >= cfg.Interval {
			runScan(client, cfg, state)
			lastFull = time.Now()
		}
	}
}

// pollCommand asks the platform whether an on-demand scan was requested for this
// agent. Errors are non-fatal; the periodic schedule still applies.
func pollCommand(client *http.Client, cfg agentConfig, state agentState) bool {
	req, err := http.NewRequest(http.MethodGet, cfg.PlatformURL+"/api/v1/agent/commands", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+state.AgentToken)
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var out struct {
		ScanNow bool `json:"scan_now"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return false
	}
	return out.ScanNow
}

// ensureEnrolled loads a saved agent token or performs enrollment with the
// installation token against the platform.
func ensureEnrolled(client *http.Client, cfg agentConfig) (agentState, error) {
	if st, ok := readState(cfg.StatePath); ok {
		return st, nil
	}
	body, _ := json.Marshal(map[string]any{
		"install_token": cfg.InstallToken,
		"name":          cfg.AgentName,
		"ranges":        cfg.Ranges,
	})
	req, _ := http.NewRequest(http.MethodPost, cfg.PlatformURL+"/api/v1/enroll", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return agentState{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return agentState{}, errStatus(resp)
	}
	var st agentState
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return agentState{}, err
	}
	writeState(cfg.StatePath, st)
	return st, nil
}

// runScan executes a full discovery pass: nmap host/port scan, ARP enrichment,
// DNS enumeration, then uploads the report to the platform.
func runScan(client *http.Client, cfg agentConfig, state agentState) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	flags := scanner.DefaultFlags(cfg.NmapFlags)
	log.Printf("scanning %d range(s) with: nmap %s", len(cfg.Ranges), strings.Join(flags, " "))

	hosts, err := scanner.RunNmap(ctx, cfg.NmapBinary, flags, cfg.Ranges)
	if err != nil {
		log.Printf("nmap error: %v", err)
	}

	arp := scanner.ReadARPTable()
	hosts = scanner.MergeARP(hosts, arp)
	log.Printf("discovered %d host(s), %d ARP neighbour(s)", len(hosts), len(arp))

	dns := scanner.EnumerateDNS(ctx, cfg.Resolver, hosts)
	log.Printf("DNS enumeration produced %d record(s)", len(dns))

	report := buildReport(hosts, dns)
	if err := uploadReport(client, cfg, state, report); err != nil {
		log.Printf("upload failed: %v", err)
		return
	}
	log.Printf("report uploaded: %d hosts, %d dns records", len(report.Hosts), len(report.DNS))
}
