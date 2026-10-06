package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/valid/ntm/agent/scanner"
)

// agentConfig is sourced from environment variables set at install time.
type agentConfig struct {
	PlatformURL  string // e.g. https://ntm-platform:8443
	InstallToken string // pasted by the operator during installation
	AgentName    string
	Ranges       []string
	NmapBinary     string
	NmapFlags      string
	DiscoveryFlags string
	Resolver       string
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
	poll, err := time.ParseDuration(env("NTM_POLL_INTERVAL", "5s"))
	if err != nil {
		poll = 5 * time.Second
	}
	return agentConfig{
		PlatformURL:  strings.TrimRight(env("NTM_PLATFORM_URL", "https://localhost:8443"), "/"),
		InstallToken: env("NTM_INSTALL_TOKEN", ""),
		AgentName:    env("NTM_AGENT_NAME", hostnameOr("ntm-agent")),
		Ranges:       splitCSV(ranges),
		NmapBinary:     env("NTM_NMAP_BIN", "nmap"),
		NmapFlags:      env("NTM_NMAP_FLAGS", ""),
		DiscoveryFlags: env("NTM_DISCOVERY_FLAGS", ""),
		Resolver:       env("NTM_DNS_RESOLVER", ""),
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

	r := &runner{client: client, cfg: cfg, state: state}
	r.start("startup")             // initial scan
	lastFull := time.Now()
	log.Printf("polling for commands every %s; periodic scan every %s", cfg.PollInterval, cfg.Interval)

	// A single poll loop drives both on-demand and scheduled scans. Because the
	// scan runs in its own goroutine, polling continues during a scan so a STOP
	// command can cancel the running nmap process.
	for {
		time.Sleep(cfg.PollInterval)
		scanNow, stopNow := pollCommand(client, cfg, state)
		if stopNow {
			r.stop()
		}
		if scanNow {
			if r.start("on-demand") {
				lastFull = time.Now()
			}
		}
		if time.Since(lastFull) >= cfg.Interval && !r.isRunning() {
			r.start("scheduled")
			lastFull = time.Now()
		}
	}
}

// runner serializes scan execution and exposes a cancel handle so an in-flight
// nmap can be killed on demand.
type runner struct {
	client  *http.Client
	cfg     agentConfig
	state   agentState
	mu      sync.Mutex
	cancel  context.CancelFunc
	running bool
}

func (r *runner) isRunning() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

// start launches a scan unless one is already running. Returns true if a scan
// was started.
func (r *runner) start(trigger string) bool {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		log.Printf("scan already running; ignoring %s trigger", trigger)
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	r.cancel = cancel
	r.running = true
	r.mu.Unlock()

	log.Printf("starting scan (%s)", trigger)
	go func() {
		defer func() {
			cancel()
			r.mu.Lock()
			r.running = false
			r.cancel = nil
			r.mu.Unlock()
		}()
		runScan(ctx, r.client, r.cfg, r.state)
	}()
	return true
}

// stop cancels the in-flight scan context, which kills the nmap child process
// (exec.CommandContext sends SIGKILL on cancel).
func (r *runner) stop() {
	r.mu.Lock()
	cancel := r.cancel
	running := r.running
	r.mu.Unlock()
	if running && cancel != nil {
		log.Printf("STOP requested — killing running scan")
		cancel()
	}
}

// pollCommand asks the platform for pending scan/stop commands. Errors are
// non-fatal; the periodic schedule still applies.
func pollCommand(client *http.Client, cfg agentConfig, state agentState) (scanNow, stopNow bool) {
	req, err := http.NewRequest(http.MethodGet, cfg.PlatformURL+"/api/v1/agent/commands", nil)
	if err != nil {
		return false, false
	}
	req.Header.Set("Authorization", "Bearer "+state.AgentToken)
	resp, err := client.Do(req)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, false
	}
	var out struct {
		ScanNow bool `json:"scan_now"`
		StopNow bool `json:"stop_now"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return false, false
	}
	return out.ScanNow, out.StopNow
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
// DNS enumeration, then uploads the report to the platform. The context lets an
// on-demand STOP cancel (kill) the running nmap mid-scan.
func runScan(ctx context.Context, client *http.Client, cfg agentConfig, state agentState) {
	reportStatus(client, cfg, state, "running")

	// Phase 1 — fast host discovery across the (possibly large) ranges.
	discFlags := scanner.DiscoveryFlags(cfg.DiscoveryFlags)
	log.Printf("phase 1/2: host discovery on %d range(s) with: nmap %s",
		len(cfg.Ranges), strings.Join(discFlags, " "))
	live, err := scanner.Discover(ctx, cfg.NmapBinary, discFlags, cfg.Ranges)
	if err != nil {
		log.Printf("discovery error: %v", err)
	}
	log.Printf("phase 1/2: %d live host(s) found", len(live))

	// Phase 2 — detailed service scan of only the live hosts.
	var hosts []scanner.Host
	if len(live) > 0 {
		flags := scanner.DefaultFlags(cfg.NmapFlags)
		log.Printf("phase 2/2: service scan of %d host(s) with: nmap %s",
			len(live), strings.Join(flags, " "))
		hosts, err = scanner.RunNmap(ctx, cfg.NmapBinary, flags, live)
		if err != nil {
			log.Printf("nmap error: %v", err)
		}
	}

	// If the scan was stopped/cancelled, discard partial results so a STOP never
	// degrades the existing inventory; just return the agent to idle.
	if ctx.Err() != nil {
		log.Printf("scan cancelled — discarding partial results")
		reportStatus(client, cfg, state, "idle")
		return
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
