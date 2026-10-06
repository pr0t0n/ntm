package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/valid/ntm/platform/internal/models"
)

type Store struct {
	pool *pgxpool.Pool
}

// Open connects to Postgres, retrying so the platform can start alongside the
// database container, and applies the schema.
func Open(ctx context.Context, url string) (*Store, error) {
	var pool *pgxpool.Pool
	var err error
	for i := 0; i < 30; i++ {
		pool, err = pgxpool.New(ctx, url)
		if err == nil {
			if err = pool.Ping(ctx); err == nil {
				break
			}
		}
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	s := &Store{pool: pool}
	if err := s.migrate(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, schema); err != nil {
		return err
	}
	// Idempotent columns for on-demand scanning and run state.
	_, err := s.pool.Exec(ctx, `
		ALTER TABLE agents ADD COLUMN IF NOT EXISTS scan_requested BOOLEAN NOT NULL DEFAULT false;
		ALTER TABLE agents ADD COLUMN IF NOT EXISTS stop_requested BOOLEAN NOT NULL DEFAULT false;
		ALTER TABLE agents ADD COLUMN IF NOT EXISTS scan_state TEXT NOT NULL DEFAULT 'idle';
		ALTER TABLE agents ADD COLUMN IF NOT EXISTS scan_started_at TIMESTAMPTZ;
		ALTER TABLE agents ADD COLUMN IF NOT EXISTS last_scan_at TIMESTAMPTZ;`)
	return err
}

const schema = `
CREATE TABLE IF NOT EXISTS agents (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL,
    agent_token TEXT NOT NULL UNIQUE,
    ranges      TEXT NOT NULL DEFAULT '',
    last_seen   TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS hosts (
    id         BIGSERIAL PRIMARY KEY,
    ip         TEXT NOT NULL UNIQUE,
    hostname   TEXT NOT NULL DEFAULT '',
    status     TEXT NOT NULL DEFAULT 'up',
    mac        TEXT NOT NULL DEFAULT '',
    vendor     TEXT NOT NULL DEFAULT '',
    os_guess   TEXT NOT NULL DEFAULT '',
    asset_type TEXT NOT NULL DEFAULT 'unknown',
    agent_id   BIGINT,
    first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS ports (
    id       BIGSERIAL PRIMARY KEY,
    host_id  BIGINT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    port     INT NOT NULL,
    protocol TEXT NOT NULL DEFAULT 'tcp',
    state    TEXT NOT NULL DEFAULT 'open',
    service  TEXT NOT NULL DEFAULT '',
    product  TEXT NOT NULL DEFAULT '',
    version  TEXT NOT NULL DEFAULT '',
    UNIQUE (host_id, port, protocol)
);

CREATE TABLE IF NOT EXISTS dns_records (
    id      BIGSERIAL PRIMARY KEY,
    host_ip TEXT NOT NULL DEFAULT '',
    name    TEXT NOT NULL,
    type    TEXT NOT NULL,
    value   TEXT NOT NULL,
    UNIQUE (name, type, value)
);

CREATE INDEX IF NOT EXISTS idx_hosts_hostname ON hosts (lower(hostname));
CREATE INDEX IF NOT EXISTS idx_ports_service  ON ports (lower(service));
CREATE INDEX IF NOT EXISTS idx_ports_port     ON ports (port);
`

// --- Agents ---

func newToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Store) CreateAgent(ctx context.Context, name string, ranges []string) (models.Agent, error) {
	token := newToken()
	var a models.Agent
	err := s.pool.QueryRow(ctx,
		`INSERT INTO agents (name, agent_token, ranges, last_seen)
		 VALUES ($1,$2,$3,now())
		 RETURNING id, name, agent_token, created_at`,
		name, token, strings.Join(ranges, ",")).
		Scan(&a.ID, &a.Name, &a.AgentToken, &a.CreatedAt)
	if err != nil {
		return a, err
	}
	a.Ranges = ranges
	return a, nil
}

func (s *Store) AgentByToken(ctx context.Context, token string) (models.Agent, error) {
	var a models.Agent
	var ranges string
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, agent_token, ranges, COALESCE(last_seen, now()), created_at
		 FROM agents WHERE agent_token=$1`, token).
		Scan(&a.ID, &a.Name, &a.AgentToken, &ranges, &a.LastSeen, &a.CreatedAt)
	if err != nil {
		return a, err
	}
	if ranges != "" {
		a.Ranges = strings.Split(ranges, ",")
	}
	return a, nil
}

func (s *Store) TouchAgent(ctx context.Context, id int64) {
	_, _ = s.pool.Exec(ctx, `UPDATE agents SET last_seen=now() WHERE id=$1`, id)
}

func (s *Store) CountAgents(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM agents`).Scan(&n)
	return n, err
}

// ListAgents returns all enrolled agents with scheduling metadata for the UI.
func (s *Store) ListAgents(ctx context.Context) ([]models.Agent, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, ranges, COALESCE(last_seen, 'epoch'::timestamptz),
		        last_scan_at, scan_requested, scan_state, scan_started_at, created_at
		 FROM agents ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Agent
	for rows.Next() {
		var a models.Agent
		var ranges string
		var lastScan, startedAt *time.Time
		if err := rows.Scan(&a.ID, &a.Name, &ranges, &a.LastSeen, &lastScan,
			&a.ScanRequested, &a.ScanState, &startedAt, &a.CreatedAt); err != nil {
			return nil, err
		}
		if ranges != "" {
			a.Ranges = strings.Split(ranges, ",")
		}
		if lastScan != nil {
			a.LastScanAt = *lastScan
		}
		if startedAt != nil {
			a.ScanStartedAt = *startedAt
		}
		a.Online = time.Since(a.LastSeen) < 90*time.Second
		out = append(out, a)
	}
	return out, rows.Err()
}

// RequestScan flags an agent to run a scan on its next poll.
func (s *Store) RequestScan(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `UPDATE agents SET scan_requested=true WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RequestStop flags a running scan to be aborted on the agent's next poll and
// cancels any still-pending run request.
func (s *Store) RequestStop(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `UPDATE agents SET stop_requested=true, scan_requested=false WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ConsumeCommands atomically returns and clears pending scan/stop commands. When
// a scan is granted, the agent is moved into the 'running' state with a start
// timestamp so the UI can show elapsed time.
func (s *Store) ConsumeCommands(ctx context.Context, id int64) (scanNow, stopNow bool, err error) {
	var dummy bool
	e1 := s.pool.QueryRow(ctx,
		`UPDATE agents SET scan_requested=false
		 WHERE id=$1 AND scan_requested=true RETURNING true`, id).Scan(&dummy)
	if e1 == nil {
		scanNow = true
	} else if !errors.Is(e1, pgx.ErrNoRows) {
		return false, false, e1
	}

	e2 := s.pool.QueryRow(ctx,
		`UPDATE agents SET stop_requested=false WHERE id=$1 AND stop_requested=true RETURNING true`, id).Scan(&dummy)
	if e2 == nil {
		stopNow = true
	} else if !errors.Is(e2, pgx.ErrNoRows) {
		return scanNow, false, e2
	}
	return scanNow, stopNow, nil
}

// SetRunning marks an agent as actively scanning (reported by the agent when a
// scan begins, regardless of whether it was on-demand or scheduled).
func (s *Store) SetRunning(ctx context.Context, id int64) {
	_, _ = s.pool.Exec(ctx,
		`UPDATE agents SET scan_state='running', scan_started_at=now() WHERE id=$1`, id)
}

// MarkScanDone records that an agent finished (uploaded a report), returning to
// the idle state.
func (s *Store) MarkScanDone(ctx context.Context, id int64) {
	_, _ = s.pool.Exec(ctx,
		`UPDATE agents SET last_scan_at=now(), scan_state='idle',
		        scan_started_at=NULL, stop_requested=false WHERE id=$1`, id)
}

// --- Hosts / ingest ---

// UpsertHost inserts or updates a host and replaces its open-port set. The
// asset_type and os_guess are computed by the caller via the classifier.
func (s *Store) UpsertHost(ctx context.Context, h models.ReportedHost, assetType, osGuess string, agentID int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var hostID int64
	err = tx.QueryRow(ctx,
		`INSERT INTO hosts (ip, hostname, status, mac, vendor, os_guess, asset_type, agent_id, last_seen)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8, now())
		 ON CONFLICT (ip) DO UPDATE SET
		    hostname = CASE WHEN EXCLUDED.hostname <> '' THEN EXCLUDED.hostname ELSE hosts.hostname END,
		    status   = EXCLUDED.status,
		    mac      = CASE WHEN EXCLUDED.mac <> '' THEN EXCLUDED.mac ELSE hosts.mac END,
		    vendor   = CASE WHEN EXCLUDED.vendor <> '' THEN EXCLUDED.vendor ELSE hosts.vendor END,
		    os_guess = CASE WHEN EXCLUDED.os_guess <> '' THEN EXCLUDED.os_guess ELSE hosts.os_guess END,
		    asset_type = EXCLUDED.asset_type,
		    agent_id = EXCLUDED.agent_id,
		    last_seen = now()
		 RETURNING id`,
		h.IP, h.Hostname, status(h.Status), h.MAC, h.Vendor, osGuess, assetType, agentID).
		Scan(&hostID)
	if err != nil {
		return err
	}

	if _, err = tx.Exec(ctx, `DELETE FROM ports WHERE host_id=$1`, hostID); err != nil {
		return err
	}
	for _, p := range h.Ports {
		if _, err = tx.Exec(ctx,
			`INSERT INTO ports (host_id, port, protocol, state, service, product, version)
			 VALUES ($1,$2,$3,$4,$5,$6,$7)
			 ON CONFLICT (host_id, port, protocol) DO NOTHING`,
			hostID, p.Port, proto(p.Protocol), p.State, p.Service, p.Product, p.Version); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) InsertDNS(ctx context.Context, r models.DNSRecord) error {
	if r.Name == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO dns_records (host_ip, name, type, value)
		 VALUES ($1,$2,$3,$4) ON CONFLICT (name, type, value) DO NOTHING`,
		r.HostIP, r.Name, r.Type, r.Value)
	return err
}

// --- CMDB search ---

// SearchHosts filters the inventory by a field (ip | hostname | app | port) and
// a query string. An empty query returns all hosts.
func (s *Store) SearchHosts(ctx context.Context, field, q string) ([]models.Host, error) {
	var (
		where string
		args  []any
	)
	q = strings.TrimSpace(q)
	switch field {
	case "ip":
		where = `h.ip ILIKE $1`
		args = []any{"%" + q + "%"}
	case "hostname":
		where = `h.hostname ILIKE $1`
		args = []any{"%" + q + "%"}
	case "app":
		where = `EXISTS (SELECT 1 FROM ports p WHERE p.host_id=h.id
		          AND (p.service ILIKE $1 OR p.product ILIKE $1))`
		args = []any{"%" + q + "%"}
	case "port":
		where = `EXISTS (SELECT 1 FROM ports p WHERE p.host_id=h.id AND p.port::text = $1)`
		args = []any{q}
	default: // all fields
		if q == "" {
			where = `TRUE`
		} else {
			where = `(h.ip ILIKE $1 OR h.hostname ILIKE $1 OR h.os_guess ILIKE $1
			          OR EXISTS (SELECT 1 FROM ports p WHERE p.host_id=h.id
			             AND (p.service ILIKE $1 OR p.product ILIKE $1 OR p.port::text = $2)))`
			args = []any{"%" + q + "%", q}
		}
	}

	rows, err := s.pool.Query(ctx,
		`SELECT h.id,h.ip,h.hostname,h.status,h.mac,h.vendor,h.os_guess,h.asset_type,
		        COALESCE(h.agent_id,0),h.first_seen,h.last_seen
		 FROM hosts h WHERE `+where+` ORDER BY h.ip LIMIT 1000`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hosts []models.Host
	for rows.Next() {
		var h models.Host
		if err := rows.Scan(&h.ID, &h.IP, &h.Hostname, &h.Status, &h.MAC, &h.Vendor,
			&h.OSGuess, &h.AssetType, &h.AgentID, &h.FirstSeen, &h.LastSeen); err != nil {
			return nil, err
		}
		hosts = append(hosts, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return s.attachPorts(ctx, hosts)
}

func (s *Store) attachPorts(ctx context.Context, hosts []models.Host) ([]models.Host, error) {
	byID := make(map[int64]int, len(hosts))
	ids := make([]int64, 0, len(hosts))
	for i := range hosts {
		byID[hosts[i].ID] = i
		ids = append(ids, hosts[i].ID)
	}
	if len(ids) == 0 {
		return hosts, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id,host_id,port,protocol,state,service,product,version
		 FROM ports WHERE host_id = ANY($1) ORDER BY port`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p models.Port
		if err := rows.Scan(&p.ID, &p.HostID, &p.Port, &p.Protocol, &p.State,
			&p.Service, &p.Product, &p.Version); err != nil {
			return nil, err
		}
		if i, ok := byID[p.HostID]; ok {
			hosts[i].Ports = append(hosts[i].Ports, p)
		}
	}
	return hosts, rows.Err()
}

func (s *Store) HostByID(ctx context.Context, id int64) (models.Host, error) {
	var h models.Host
	err := s.pool.QueryRow(ctx,
		`SELECT id,ip,hostname,status,mac,vendor,os_guess,asset_type,
		        COALESCE(agent_id,0),first_seen,last_seen
		 FROM hosts WHERE id=$1`, id).
		Scan(&h.ID, &h.IP, &h.Hostname, &h.Status, &h.MAC, &h.Vendor,
			&h.OSGuess, &h.AssetType, &h.AgentID, &h.FirstSeen, &h.LastSeen)
	if errors.Is(err, pgx.ErrNoRows) {
		return h, ErrNotFound
	}
	if err != nil {
		return h, err
	}
	out, err := s.attachPorts(ctx, []models.Host{h})
	if err != nil {
		return h, err
	}
	return out[0], nil
}

var ErrNotFound = errors.New("not found")

// --- Dashboard ---

func (s *Store) Dashboard(ctx context.Context) (models.DashboardStats, error) {
	var d models.DashboardStats
	d.ByAssetType = map[string]int{}
	d.ByOS = map[string]int{}

	if err := s.pool.QueryRow(ctx,
		`SELECT count(*),
		        count(*) FILTER (WHERE status='up'),
		        count(*) FILTER (WHERE status='down')
		 FROM hosts`).Scan(&d.TotalHosts, &d.HostsUp, &d.HostsDown); err != nil {
		return d, err
	}

	if err := scanCounts(ctx, s, `SELECT asset_type, count(*) FROM hosts GROUP BY asset_type`, d.ByAssetType); err != nil {
		return d, err
	}
	if err := scanCounts(ctx, s,
		`SELECT CASE WHEN os_guess='' THEN 'unknown' ELSE os_guess END, count(*)
		 FROM hosts GROUP BY 1`, d.ByOS); err != nil {
		return d, err
	}

	rows, err := s.pool.Query(ctx,
		`SELECT CASE WHEN service='' THEN 'unknown' ELSE service END AS app, count(*) c
		 FROM ports WHERE state='open' GROUP BY app ORDER BY c DESC LIMIT 10`)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var ac models.AppCount
		if err := rows.Scan(&ac.Name, &ac.Count); err != nil {
			rows.Close()
			return d, err
		}
		d.TopApps = append(d.TopApps, ac)
	}
	rows.Close()

	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM ports WHERE state='open'`).Scan(&d.OpenPorts)
	d.Agents, _ = s.CountAgents(ctx)

	recent, err := s.SearchHosts(ctx, "all", "")
	if err == nil {
		if len(recent) > 8 {
			recent = recent[:8]
		}
		d.Recent = recent
	}
	return d, nil
}

func scanCounts(ctx context.Context, s *Store, query string, into map[string]int) error {
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var c int
		if err := rows.Scan(&k, &c); err != nil {
			return err
		}
		into[k] = c
	}
	return rows.Err()
}

// --- Network map ---

// MapData returns hosts as nodes plus edges linking every host to its subnet
// gateway and the subnets up to the Internet edge.
func (s *Store) MapData(ctx context.Context) (map[string]any, error) {
	hosts, err := s.SearchHosts(ctx, "all", "")
	if err != nil {
		return nil, err
	}
	return BuildTopology(hosts), nil
}

func status(s string) string {
	if s == "down" {
		return "down"
	}
	return "up"
}

func proto(p string) string {
	if p == "" {
		return "tcp"
	}
	return p
}
