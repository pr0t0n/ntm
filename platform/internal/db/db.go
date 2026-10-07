package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
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
		ALTER TABLE agents ADD COLUMN IF NOT EXISTS last_scan_at TIMESTAMPTZ;
		ALTER TABLE agents ADD COLUMN IF NOT EXISTS scan_phase TEXT NOT NULL DEFAULT '';
		ALTER TABLE agents ADD COLUMN IF NOT EXISTS phase_started_at TIMESTAMPTZ;
		ALTER TABLE agents ADD COLUMN IF NOT EXISTS scan_hosts INT NOT NULL DEFAULT 0;
		ALTER TABLE agents ADD COLUMN IF NOT EXISTS ip TEXT NOT NULL DEFAULT '';
		ALTER TABLE agents ADD COLUMN IF NOT EXISTS scan_range TEXT NOT NULL DEFAULT '';
		CREATE TABLE IF NOT EXISTS install_tokens (
		    token      TEXT PRIMARY KEY,
		    label      TEXT NOT NULL DEFAULT '',
		    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		    used_at    TIMESTAMPTZ,
		    used_by    BIGINT
		);
		CREATE TABLE IF NOT EXISTS changes (
		    id      BIGSERIAL PRIMARY KEY,
		    ts      TIMESTAMPTZ NOT NULL DEFAULT now(),
		    host_ip TEXT NOT NULL,
		    kind    TEXT NOT NULL,
		    detail  TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX IF NOT EXISTS idx_changes_ts ON changes (ts DESC);
		CREATE INDEX IF NOT EXISTS idx_changes_ip ON changes (host_ip);`)
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

// CreateInstallToken generates a unique, per-installation enrollment token.
func (s *Store) CreateInstallToken(ctx context.Context, label string) (string, error) {
	token := newToken()
	_, err := s.pool.Exec(ctx, `INSERT INTO install_tokens (token, label) VALUES ($1,$2)`, token, label)
	return token, err
}

// InstallTokenValid reports whether a generated install token exists (it stays
// valid so the collector can re-enroll if it loses its state).
func (s *Store) InstallTokenValid(ctx context.Context, token string) bool {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT true FROM install_tokens WHERE token=$1`, token).Scan(&ok)
	return err == nil && ok
}

// MarkInstallTokenUsed records which agent enrolled with a token (informational).
func (s *Store) MarkInstallTokenUsed(ctx context.Context, token string, agentID int64) {
	_, _ = s.pool.Exec(ctx,
		`UPDATE install_tokens SET used_at=COALESCE(used_at, now()), used_by=$2 WHERE token=$1`, token, agentID)
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

// SetAgentIP records the agent's own (self-reported) primary IP, used to suggest
// a scan range in the UI.
func (s *Store) SetAgentIP(ctx context.Context, id int64, ip string) {
	if ip == "" {
		return
	}
	_, _ = s.pool.Exec(ctx, `UPDATE agents SET ip=$2 WHERE id=$1`, id, ip)
}

// SetScanRange records the range(s) the current scan is covering, reported by
// the agent when the scan starts, so the UI can show what is being scanned.
func (s *Store) SetScanRange(ctx context.Context, id int64, r string) {
	_, _ = s.pool.Exec(ctx, `UPDATE agents SET scan_range=$2 WHERE id=$1`, id, r)
}

// DeleteAgent removes an enrolled agent. Discovered hosts are left in place.
func (s *Store) DeleteAgent(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM agents WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
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
		        last_scan_at, scan_requested, scan_state, scan_started_at, created_at,
		        scan_phase, phase_started_at, scan_hosts, ip, scan_range
		 FROM agents ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Agent
	for rows.Next() {
		var a models.Agent
		var ranges string
		var lastScan, startedAt, phaseAt *time.Time
		if err := rows.Scan(&a.ID, &a.Name, &ranges, &a.LastSeen, &lastScan,
			&a.ScanRequested, &a.ScanState, &startedAt, &a.CreatedAt,
			&a.ScanPhase, &phaseAt, &a.ScanHosts, &a.IP, &a.ScanRange); err != nil {
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
		if phaseAt != nil {
			a.PhaseStartedAt = *phaseAt
		}
		a.Online = time.Since(a.LastSeen) < 90*time.Second
		out = append(out, a)
	}
	return out, rows.Err()
}

// RequestScan flags an agent to run a scan on its next poll, over the given
// range (empty = the agent's configured ranges).
func (s *Store) RequestScan(ctx context.Context, id int64, scanRange string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE agents SET scan_requested=true, scan_range=$2 WHERE id=$1`, id, scanRange)
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
func (s *Store) ConsumeCommands(ctx context.Context, id int64) (scanNow, stopNow bool, scanRange string, err error) {
	e1 := s.pool.QueryRow(ctx,
		`UPDATE agents SET scan_requested=false
		 WHERE id=$1 AND scan_requested=true RETURNING COALESCE(scan_range,'')`, id).Scan(&scanRange)
	if e1 == nil {
		scanNow = true
	} else if !errors.Is(e1, pgx.ErrNoRows) {
		return false, false, "", e1
	}

	var dummy bool
	e2 := s.pool.QueryRow(ctx,
		`UPDATE agents SET stop_requested=false WHERE id=$1 AND stop_requested=true RETURNING true`, id).Scan(&dummy)
	if e2 == nil {
		stopNow = true
	} else if !errors.Is(e2, pgx.ErrNoRows) {
		return scanNow, false, "", e2
	}
	return scanNow, stopNow, scanRange, nil
}

// SetRunning marks an agent as actively scanning (reported by the agent when a
// scan begins, regardless of whether it was on-demand or scheduled) and resets
// the per-scan phase and host counter.
func (s *Store) SetRunning(ctx context.Context, id int64) {
	_, _ = s.pool.Exec(ctx,
		`UPDATE agents SET scan_state='running', scan_started_at=now(),
		        scan_phase='', phase_started_at=NULL, scan_hosts=0 WHERE id=$1`, id)
}

// SetPhase records which discovery phase (ping | hostname | ports | detect) the
// agent has entered, so the UI can show the phase and its elapsed time.
func (s *Store) SetPhase(ctx context.Context, id int64, phase string) {
	// "ping" is the first phase of a run: reset the per-scan counter and start time.
	if phase == "ping" {
		_, _ = s.pool.Exec(ctx,
			`UPDATE agents SET scan_state='running', scan_phase=$2, phase_started_at=now(),
			        scan_started_at=now(), scan_hosts=0 WHERE id=$1`, id, phase)
		return
	}
	_, _ = s.pool.Exec(ctx,
		`UPDATE agents SET scan_state='running', scan_phase=$2, phase_started_at=now(),
		        scan_started_at=COALESCE(scan_started_at, now()) WHERE id=$1`, id, phase)
}

// AddScanHosts increments the number of live hosts found in the current scan.
func (s *Store) AddScanHosts(ctx context.Context, id int64, n int) {
	_, _ = s.pool.Exec(ctx, `UPDATE agents SET scan_hosts=scan_hosts+$2 WHERE id=$1`, id, n)
}

// MarkScanDone records that an agent finished (uploaded a report), returning to
// the idle state.
func (s *Store) MarkScanDone(ctx context.Context, id int64) {
	_, _ = s.pool.Exec(ctx,
		`UPDATE agents SET last_scan_at=now(), scan_state='idle', scan_started_at=NULL,
		        scan_phase='', phase_started_at=NULL, stop_requested=false WHERE id=$1`, id)
}

// ResetInventory wipes every discovered record (hosts, ports, DNS) and clears
// each agent's scan state. Agents stay enrolled so their tokens keep working —
// deleting them would leave running agents stuck on 401 with no re-enrollment.
func (s *Store) ResetInventory(ctx context.Context) (models.ResetResult, error) {
	var res models.ResetResult
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)

	if err := tx.QueryRow(ctx,
		`SELECT (SELECT count(*) FROM hosts), (SELECT count(*) FROM ports), (SELECT count(*) FROM dns_records)`).
		Scan(&res.Hosts, &res.Ports, &res.DNS); err != nil {
		return res, err
	}
	if _, err := tx.Exec(ctx, `TRUNCATE ports, hosts, dns_records RESTART IDENTITY`); err != nil {
		return res, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE agents SET last_scan_at=NULL, scan_state='idle', scan_started_at=NULL,
		        scan_phase='', phase_started_at=NULL, scan_hosts=0,
		        scan_requested=false, stop_requested=false`); err != nil {
		return res, err
	}
	if _, err := tx.Exec(ctx, `TRUNCATE changes RESTART IDENTITY`); err != nil {
		return res, err
	}
	return res, tx.Commit(ctx)
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

// --- Phased ingest: each discovery phase stores its own slice of host data ---

// UpsertPingHost stores a host that answered the phase-1 ping. It only touches
// liveness/L2 fields, so data from later phases of earlier scans is preserved.
// recordChange appends an entry to the change log (best-effort).
func (s *Store) recordChange(ctx context.Context, ip, kind, detail string) {
	_, _ = s.pool.Exec(ctx, `INSERT INTO changes (host_ip, kind, detail) VALUES ($1,$2,$3)`, ip, kind, detail)
}

// RecentChanges returns the latest change-log entries, optionally for one IP.
func (s *Store) RecentChanges(ctx context.Context, ip string, limit int) ([]models.Change, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows pgx.Rows
	var err error
	if ip != "" {
		rows, err = s.pool.Query(ctx, `SELECT id, ts, host_ip, kind, detail FROM changes WHERE host_ip=$1 ORDER BY ts DESC LIMIT $2`, ip, limit)
	} else {
		rows, err = s.pool.Query(ctx, `SELECT id, ts, host_ip, kind, detail FROM changes ORDER BY ts DESC LIMIT $1`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Change
	for rows.Next() {
		var c models.Change
		if err := rows.Scan(&c.ID, &c.TS, &c.HostIP, &c.Kind, &c.Detail); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) UpsertPingHost(ctx context.Context, h models.ReportedHost, agentID int64) error {
	var inserted bool
	err := s.pool.QueryRow(ctx,
		`INSERT INTO hosts (ip, status, mac, vendor, agent_id, last_seen)
		 VALUES ($1,'up',$2,$3,$4, now())
		 ON CONFLICT (ip) DO UPDATE SET
		    status   = 'up',
		    mac      = CASE WHEN EXCLUDED.mac <> '' THEN EXCLUDED.mac ELSE hosts.mac END,
		    vendor   = CASE WHEN EXCLUDED.vendor <> '' THEN EXCLUDED.vendor ELSE hosts.vendor END,
		    agent_id = EXCLUDED.agent_id,
		    last_seen = now()
		 RETURNING (xmax = 0)`,
		h.IP, h.MAC, h.Vendor, agentID).Scan(&inserted)
	if err == nil && inserted {
		s.recordChange(ctx, h.IP, "host_novo", "Host descoberto na rede")
	}
	return err
}

// SetHostname stores the phase-2 hostname of a known host.
func (s *Store) SetHostname(ctx context.Context, ip, hostname string) error {
	var old string
	_ = s.pool.QueryRow(ctx, `SELECT hostname FROM hosts WHERE ip=$1`, ip).Scan(&old)
	tag, err := s.pool.Exec(ctx,
		`UPDATE hosts SET hostname=$2, last_seen=now() WHERE ip=$1`, ip, hostname)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if hostname != "" && hostname != old {
		if old == "" {
			s.recordChange(ctx, ip, "hostname", "Nome resolvido: "+hostname)
		} else {
			s.recordChange(ctx, ip, "hostname", "Nome alterado: "+old+" -> "+hostname)
		}
	}
	return nil
}

// ReplacePorts stores the phase-3 open-port set of a known host.
func (s *Store) ReplacePorts(ctx context.Context, ip string, ports []models.ReportedPort) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var hostID int64
	err = tx.QueryRow(ctx, `UPDATE hosts SET last_seen=now() WHERE ip=$1 RETURNING id`, ip).Scan(&hostID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	// Diff the open-port set to log opened/closed ports.
	oldPorts := map[int]bool{}
	if rows, e := tx.Query(ctx, `SELECT port FROM ports WHERE host_id=$1`, hostID); e == nil {
		for rows.Next() {
			var pt int
			if rows.Scan(&pt) == nil {
				oldPorts[pt] = true
			}
		}
		rows.Close()
	}
	newPorts := map[int]bool{}
	for _, p := range ports {
		newPorts[p.Port] = true
	}
	for pt := range newPorts {
		if !oldPorts[pt] && len(oldPorts) > 0 {
			s.recordChange(ctx, ip, "porta_aberta", fmt.Sprintf("Porta %d aberta", pt))
		}
	}
	for pt := range oldPorts {
		if !newPorts[pt] {
			s.recordChange(ctx, ip, "porta_fechada", fmt.Sprintf("Porta %d fechada", pt))
		}
	}
	if _, err = tx.Exec(ctx, `DELETE FROM ports WHERE host_id=$1`, hostID); err != nil {
		return err
	}
	for _, p := range ports {
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

// MergeDetection stores phase-4 results: the OS guess and the service
// fingerprint (published platform: product/version) of each open port.
func (s *Store) MergeDetection(ctx context.Context, ip, osGuess string, ports []models.ReportedPort) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var hostID int64
	var oldOS string
	_ = tx.QueryRow(ctx, `SELECT os_guess FROM hosts WHERE ip=$1`, ip).Scan(&oldOS)
	err = tx.QueryRow(ctx,
		`UPDATE hosts SET os_guess = CASE WHEN $2 <> '' THEN $2 ELSE os_guess END, last_seen=now()
		 WHERE ip=$1 RETURNING id`, ip, osGuess).Scan(&hostID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if osGuess != "" && osGuess != oldOS {
		if oldOS == "" {
			s.recordChange(ctx, ip, "so", "SO identificado: "+osGuess)
		} else {
			s.recordChange(ctx, ip, "so", "SO alterado: "+oldOS+" -> "+osGuess)
		}
	}
	for _, p := range ports {
		if _, err = tx.Exec(ctx,
			`INSERT INTO ports (host_id, port, protocol, state, service, product, version)
			 VALUES ($1,$2,$3,$4,$5,$6,$7)
			 ON CONFLICT (host_id, port, protocol) DO UPDATE SET
			    service = CASE WHEN EXCLUDED.service <> '' THEN EXCLUDED.service ELSE ports.service END,
			    product = CASE WHEN EXCLUDED.product <> '' THEN EXCLUDED.product ELSE ports.product END,
			    version = CASE WHEN EXCLUDED.version <> '' THEN EXCLUDED.version ELSE ports.version END`,
			hostID, p.Port, proto(p.Protocol), p.State, p.Service, p.Product, p.Version); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// HostByIP returns a host with its ports, used to reclassify after a phase.
func (s *Store) HostByIP(ctx context.Context, ip string) (models.Host, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `SELECT id FROM hosts WHERE ip=$1`, ip).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.Host{}, ErrNotFound
	}
	if err != nil {
		return models.Host{}, err
	}
	return s.HostByID(ctx, id)
}

func (s *Store) SetAssetType(ctx context.Context, ip, assetType string) error {
	_, err := s.pool.Exec(ctx, `UPDATE hosts SET asset_type=$2 WHERE ip=$1`, ip, assetType)
	return err
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

	// All hosts (most-recent first) so the dashboard list can paginate over the
	// full inventory instead of a truncated preview.
	recent, err := s.SearchHosts(ctx, "all", "")
	if err == nil {
		sort.Slice(recent, func(i, j int) bool { return recent[i].LastSeen.After(recent[j].LastSeen) })
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
