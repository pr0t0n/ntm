package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strconv"
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
	PingRate       int           // phase 1: ICMP echo requests per second
	PingTimeout    time.Duration // phase 1: wait for late replies after the last request
	PortFlags      string // phase 3 (open ports)
	DetectFlags    string // phase 4 (OS + published services)
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
	pingRate, err := strconv.Atoi(env("NTM_PING_RATE", "1000"))
	if err != nil || pingRate <= 0 {
		pingRate = 1000
	}
	pingTimeout, err := time.ParseDuration(env("NTM_PING_TIMEOUT", "2s"))
	if err != nil {
		pingTimeout = 2 * time.Second
	}
	return agentConfig{
		PlatformURL:  strings.TrimRight(env("NTM_PLATFORM_URL", "https://localhost:8443"), "/"),
		InstallToken: env("NTM_INSTALL_TOKEN", ""),
		AgentName:    env("NTM_AGENT_NAME", hostnameOr("ntm-agent")),
		Ranges:       splitCSV(ranges),
		NmapBinary:     env("NTM_NMAP_BIN", "nmap"),
		PingRate:       pingRate,
		PingTimeout:    pingTimeout,
		PortFlags:      env("NTM_PORT_FLAGS", ""),
		DetectFlags:    env("NTM_DETECT_FLAGS", ""),
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

// phaseBatch is how many live hosts each phase 2-4 step handles (and uploads)
// at a time. Small batches keep nmap from timing hosts out en masse and make
// results appear on the platform progressively.
const phaseBatch = 64

// runScan executes a discovery pass in four phases, storing each phase's
// results on the platform as they are produced:
//
//	1. ping     — ICMP echo sweep of the ranges (no nmap); every live host is
//	              stored right away
//	2. hostname — reverse DNS of the live hosts
//	3. ports    — open TCP ports of the live hosts
//	4. detect   — OS and published platforms (service product/version)
//
// The context lets an on-demand STOP cancel (kill) the running nmap; whatever
// earlier batches already stored stays on the platform.
func runScan(ctx context.Context, client *http.Client, cfg agentConfig, state agentState) {
	start := time.Now()
	reportStatus(client, cfg, state, "running", "")
	upload := func(phase string, hosts []scanner.Host, dns []scanner.DNSRecord) {
		if err := uploadPhase(client, cfg, state, phase, hosts, dns); err != nil {
			log.Printf("phase %s: upload failed: %v", phase, err)
		}
	}
	stopped := func(phase string) bool {
		if ctx.Err() == nil {
			return false
		}
		log.Printf("scan stopped during %s phase — batches already stored are kept", phase)
		reportStatus(client, cfg, state, "idle", "")
		return true
	}

	// Phase 1/4 — ping. Native ICMP echo; nmap only runs after this phase.
	reportStatus(client, cfg, state, "running", "ping")
	log.Printf("phase 1/4 ping: ICMP echo to %s at %d/s", strings.Join(cfg.Ranges, " "), cfg.PingRate)
	var live []string
	err := scanner.PingSweep(ctx, cfg.Ranges, cfg.PingRate, cfg.PingTimeout, func(batch []scanner.Host) {
		batch = scanner.FillMAC(batch, scanner.ReadARPTable())
		upload("ping", batch, nil)
		for _, h := range batch {
			live = append(live, h.IP)
		}
		log.Printf("phase 1/4 ping: %d live host(s) stored so far", len(live))
	})
	if err != nil && ctx.Err() == nil {
		log.Printf("phase 1/4 ping error: %v", err)
	}
	if stopped("ping") {
		return
	}
	log.Printf("phase 1/4 ping done: %d live host(s) (%s)", len(live), time.Since(start).Round(time.Second))

	// Phase 2/4 — hostname.
	reportStatus(client, cfg, state, "running", "hostname")
	named := 0
	for _, batch := range chunk(live, phaseBatch) {
		hosts, dns := scanner.ResolveHostnames(ctx, cfg.Resolver, batch)
		if len(hosts) > 0 || len(dns) > 0 {
			upload("hostname", hosts, dns)
		}
		named += len(hosts)
		if ctx.Err() != nil {
			break
		}
	}
	if stopped("hostname") {
		return
	}
	log.Printf("phase 2/4 hostname done: %d of %d host(s) named", named, len(live))

	// Phase 3/4 — open ports. Hosts are already known to be up, so skip nmap's
	// own host discovery (-Pn).
	reportStatus(client, cfg, state, "running", "ports")
	portFlags := append(scanner.PortFlags(cfg.PortFlags), "-Pn")
	log.Printf("phase 3/4 ports with: nmap %s", strings.Join(portFlags, " "))
	openPorts := map[string][]int{}
	for _, batch := range chunk(live, phaseBatch) {
		hosts, err := scanner.RunNmap(ctx, cfg.NmapBinary, portFlags, batch)
		if err != nil && ctx.Err() == nil {
			log.Printf("phase 3/4 ports error: %v", err)
		}
		if len(hosts) > 0 {
			upload("ports", hosts, nil)
		}
		for _, h := range hosts {
			for _, p := range h.Ports {
				openPorts[h.IP] = append(openPorts[h.IP], p.Port)
			}
		}
		if ctx.Err() != nil {
			break
		}
	}
	if stopped("ports") {
		return
	}
	log.Printf("phase 3/4 ports done: %d host(s) with open ports", len(openPorts))

	// Phase 4/4 — OS + published platforms, on the hosts that have open ports
	// (service detection needs them, and OS fingerprinting needs at least one).
	reportStatus(client, cfg, state, "running", "detect")
	var withPorts []string
	for _, ip := range live {
		if len(openPorts[ip]) > 0 {
			withPorts = append(withPorts, ip)
		}
	}
	detectFlags := append(scanner.DetectFlags(cfg.DetectFlags), "-Pn")
	log.Printf("phase 4/4 detect on %d host(s) with: nmap %s -p <open ports>", len(withPorts), strings.Join(detectFlags, " "))
	detected := 0
	for _, batch := range chunk(withPorts, phaseBatch) {
		flags := append(append([]string{}, detectFlags...), "-p", portList(batch, openPorts))
		hosts, err := scanner.RunNmap(ctx, cfg.NmapBinary, flags, batch)
		if err != nil && ctx.Err() == nil {
			log.Printf("phase 4/4 detect error: %v", err)
		}
		if len(hosts) > 0 {
			upload("detect", hosts, nil)
		}
		detected += len(hosts)
		if ctx.Err() != nil {
			break
		}
	}
	if stopped("detect") {
		return
	}
	log.Printf("phase 4/4 detect done: %d host(s) fingerprinted", detected)

	reportStatus(client, cfg, state, "idle", "")
	log.Printf("scan complete in %s: %d live, %d named, %d with open ports, %d fingerprinted",
		time.Since(start).Round(time.Second), len(live), named, len(openPorts), detected)
}

// chunk splits items into consecutive slices of at most n elements.
func chunk(items []string, n int) [][]string {
	var out [][]string
	for len(items) > n {
		out = append(out, items[:n])
		items = items[n:]
	}
	if len(items) > 0 {
		out = append(out, items)
	}
	return out
}

// portList is the comma-separated union of the open ports of the given hosts.
func portList(ips []string, open map[string][]int) string {
	seen := map[int]bool{}
	var ports []int
	for _, ip := range ips {
		for _, p := range open[ip] {
			if !seen[p] {
				seen[p] = true
				ports = append(ports, p)
			}
		}
	}
	sort.Ints(ports)
	parts := make([]string, len(ports))
	for i, p := range ports {
		parts[i] = strconv.Itoa(p)
	}
	return strings.Join(parts, ",")
}
