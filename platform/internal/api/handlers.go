package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/valid/ntm/platform/internal/config"
	"github.com/valid/ntm/platform/internal/db"
	"github.com/valid/ntm/platform/internal/models"
)

type Server struct {
	store *db.Store
	cfg   config.Config
}

func New(store *db.Store, cfg config.Config) *Server {
	return &Server{store: store, cfg: cfg}
}

// Routes wires the HTTP API plus the embedded static web UI.
func (s *Server) Routes(web http.Handler) http.Handler {
	mux := http.NewServeMux()

	// Agent-facing endpoints (HTTPS/TLS).
	mux.HandleFunc("POST /api/v1/enroll", s.handleEnroll)
	mux.HandleFunc("POST /api/v1/scan-results", s.authAgent(s.handleScanResults))
	mux.HandleFunc("GET /api/v1/agent/commands", s.authAgent(s.handleCommands))
	mux.HandleFunc("POST /api/v1/agent/status", s.authAgent(s.handleAgentStatus))

	// Agent management (frontend).
	mux.HandleFunc("GET /api/v1/agents", s.handleAgents)
	mux.HandleFunc("POST /api/v1/agents/{id}/run", s.handleRunAgent)
	mux.HandleFunc("POST /api/v1/agents/{id}/stop", s.handleStopAgent)

	// Frontend endpoints.
	mux.HandleFunc("GET /api/v1/dashboard", s.handleDashboard)
	mux.HandleFunc("GET /api/v1/hosts", s.handleHosts)
	mux.HandleFunc("GET /api/v1/hosts/{id}", s.handleHost)
	mux.HandleFunc("GET /api/v1/network-map", s.handleMap)
	mux.HandleFunc("GET /api/v1/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.Handle("/", web)
	return logging(mux)
}

// --- Agent endpoints ---

func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	var req models.EnrollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.InstallToken != s.cfg.InstallToken {
		httpError(w, http.StatusUnauthorized, "invalid install token")
		return
	}
	name := req.Name
	if name == "" {
		name = "agent"
	}
	agent, err := s.store.CreateAgent(r.Context(), name, req.Ranges)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "could not enroll")
		return
	}
	writeJSON(w, http.StatusOK, models.EnrollResponse{
		AgentID:    agent.ID,
		AgentToken: agent.AgentToken,
	})
}

// authAgent validates the agent bearer token and injects the agent into context.
func (s *Server) authAgent(next func(http.ResponseWriter, *http.Request, models.Agent)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearer(r)
		if token == "" {
			httpError(w, http.StatusUnauthorized, "missing token")
			return
		}
		agent, err := s.store.AgentByToken(r.Context(), token)
		if err != nil {
			httpError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		s.store.TouchAgent(r.Context(), agent.ID)
		next(w, r, agent)
	}
}

func (s *Server) handleScanResults(w http.ResponseWriter, r *http.Request, agent models.Agent) {
	var report models.ScanReport
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		httpError(w, http.StatusBadRequest, "invalid body")
		return
	}
	ctx := r.Context()
	ingested := 0
	for _, h := range report.Hosts {
		assetType := classifyAsset(h)
		osGuess := normalizeOS(h.OSGuess)
		if err := s.store.UpsertHost(ctx, h, assetType, osGuess, agent.ID); err != nil {
			log.Printf("upsert host %s: %v", h.IP, err)
			continue
		}
		ingested++
	}
	for _, d := range report.DNS {
		if err := s.store.InsertDNS(ctx, d); err != nil {
			log.Printf("insert dns %s: %v", d.Name, err)
		}
	}
	s.store.MarkScanDone(ctx, agent.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"accepted":  true,
		"hosts":     ingested,
		"dns":       len(report.DNS),
		"agent_id":  agent.ID,
	})
}

// handleCommands lets a polling agent learn whether an on-demand scan was
// requested for it from the UI; the request is consumed (cleared) atomically.
func (s *Server) handleCommands(w http.ResponseWriter, r *http.Request, agent models.Agent) {
	scanNow, stopNow, err := s.store.ConsumeCommands(r.Context(), agent.ID)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "command error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scan_now": scanNow, "stop_now": stopNow})
}

// handleAgentStatus lets the agent report when a scan starts so the UI reflects
// the running state for scheduled and startup scans too (not just on-demand).
func (s *Server) handleAgentStatus(w http.ResponseWriter, r *http.Request, agent models.Agent) {
	var body struct {
		State string `json:"state"` // running | idle
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.State == "running" {
		s.store.SetRunning(r.Context(), agent.ID)
	} else {
		s.store.MarkScanDone(r.Context(), agent.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- Agent management (frontend) ---

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := s.store.ListAgents(r.Context())
	if err != nil {
		httpError(w, http.StatusInternalServerError, "agents error")
		return
	}
	if agents == nil {
		agents = []models.Agent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
}

func (s *Server) handleRunAgent(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		httpError(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := s.store.RequestScan(r.Context(), id); errors.Is(err, db.ErrNotFound) {
		httpError(w, http.StatusNotFound, "agent not found")
		return
	} else if err != nil {
		httpError(w, http.StatusInternalServerError, "could not request scan")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"queued": true, "agent_id": id})
}

func (s *Server) handleStopAgent(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		httpError(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := s.store.RequestStop(r.Context(), id); errors.Is(err, db.ErrNotFound) {
		httpError(w, http.StatusNotFound, "agent not found")
		return
	} else if err != nil {
		httpError(w, http.StatusInternalServerError, "could not request stop")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stopping": true, "agent_id": id})
}

// --- Frontend endpoints ---

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.Dashboard(r.Context())
	if err != nil {
		httpError(w, http.StatusInternalServerError, "dashboard error")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleHosts(w http.ResponseWriter, r *http.Request) {
	field := r.URL.Query().Get("field") // ip|hostname|app|port|all
	q := r.URL.Query().Get("q")
	if field == "" {
		field = "all"
	}
	hosts, err := s.store.SearchHosts(r.Context(), field, q)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "search error")
		return
	}
	if hosts == nil {
		hosts = []models.Host{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(hosts), "hosts": hosts})
}

func (s *Server) handleHost(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		httpError(w, http.StatusBadRequest, "bad id")
		return
	}
	h, err := s.store.HostByID(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		httpError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, "error")
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) handleMap(w http.ResponseWriter, r *http.Request) {
	data, err := s.store.MapData(r.Context())
	if err != nil {
		httpError(w, http.StatusInternalServerError, "map error")
		return
	}
	writeJSON(w, http.StatusOK, data)
}

// --- helpers ---

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s %s", r.RemoteAddr, r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

var _ = context.Background
