package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Itz-snj/MSclaudeConnector/internal/protocol"
	_ "modernc.org/sqlite"
)

// Store is the authoritative local event log and device registry.
type Store struct {
	db *sql.DB
}

// Session represents a single agent session owned by this host.
type Session struct {
	ID         string
	AgentType  string
	WorkingDir string
	Status     string
	LastSeq    int64
	CreatedAt  time.Time
}

// Device represents a paired client.
type Device struct {
	DeviceID       string
	Name           string
	CredentialHash string
	PairedAt       time.Time
	LastSeen       *time.Time
	Revoked        bool
}

// Snapshot is the materialized state derived from the event log.
type Snapshot struct {
	SessionID          string
	Status             string
	Mode               string
	LastSeq            int64
	PendingPermissions []protocol.PermissionView
	PendingQuestions   []protocol.QuestionView
}

// Open opens (and creates) the SQLite database at dbPath.
func Open(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}
	// WAL + busy timeout keep concurrent readers/writers from stepping on each
	// other while the hub ingests adapter events and handles commands.
	_, _ = db.Exec("PRAGMA journal_mode=WAL")
	_, _ = db.Exec("PRAGMA busy_timeout=5000")

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    agent_type TEXT NOT NULL,
    working_dir TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'idle',
    last_seq INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS events (
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    seq INTEGER NOT NULL,
    ts INTEGER NOT NULL,
    kind TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    PRIMARY KEY (session_id, seq)
);

CREATE INDEX IF NOT EXISTS idx_events_session_seq ON events(session_id, seq);

CREATE TABLE IF NOT EXISTS devices (
    device_id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    credential_hash TEXT NOT NULL,
    paired_at INTEGER NOT NULL,
    last_seen INTEGER,
    revoked INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_devices_active ON devices(device_id, revoked);

CREATE TABLE IF NOT EXISTS pending_tokens (
    token TEXT PRIMARY KEY,
    name TEXT,
    approved INTEGER NOT NULL DEFAULT 0,
    expires_at INTEGER NOT NULL
);
`
	_, err := s.db.Exec(schema)
	return err
}

// CreateSession creates a new session and returns it.
func (s *Store) CreateSession(agentType, workingDir string) (*Session, error) {
	id, err := randomID(16)
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	_, err = s.db.Exec(
		`INSERT INTO sessions (id, agent_type, working_dir, status, last_seq, created_at) VALUES (?, ?, ?, 'idle', 0, ?)`,
		id, agentType, workingDir, now,
	)
	if err != nil {
		return nil, err
	}
	return &Session{ID: id, AgentType: agentType, WorkingDir: workingDir, Status: "idle", LastSeq: 0, CreatedAt: time.UnixMilli(now)}, nil
}

func (s *Store) GetSession(id string) (*Session, error) {
	row := s.db.QueryRow(`SELECT id, agent_type, working_dir, status, last_seq, created_at FROM sessions WHERE id = ?`, id)
	return scanSession(row)
}

func (s *Store) ListSessions() ([]*Session, error) {
	rows, err := s.db.Query(`SELECT id, agent_type, working_dir, status, last_seq, created_at FROM sessions ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

func scanSession(s interface {
	Scan(dest ...interface{}) error
}) (*Session, error) {
	var sess Session
	var created int64
	err := s.Scan(&sess.ID, &sess.AgentType, &sess.WorkingDir, &sess.Status, &sess.LastSeq, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sess.CreatedAt = time.UnixMilli(created)
	return &sess, nil
}

// AppendEvent atomically increments the session seq and stores the event.
func (s *Store) AppendEvent(sessionID string, kind protocol.EventKind, payload interface{}) (*protocol.EventPayload, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var seq int64
	err = tx.QueryRow(`UPDATE sessions SET last_seq = last_seq + 1 WHERE id = ? RETURNING last_seq`, sessionID).Scan(&seq)
	if err != nil {
		return nil, err
	}

	ts := time.Now().UnixMilli()
	_, err = tx.Exec(
		`INSERT INTO events (session_id, seq, ts, kind, payload_json) VALUES (?, ?, ?, ?, ?)`,
		sessionID, seq, ts, string(kind), payloadJSON,
	)
	if err != nil {
		return nil, err
	}

	// Keep the denormalized sessions.status fresh for cheap status queries.
	if kind == protocol.EventStatusChange {
		var body protocol.StatusChangeBody
		if err := json.Unmarshal(payloadJSON, &body); err == nil {
			_, _ = tx.Exec(`UPDATE sessions SET status = ? WHERE id = ?`, body.Status, sessionID)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	ev := &protocol.EventPayload{
		Seq:     seq,
		TS:      ts,
		Kind:    kind,
		Payload: json.RawMessage(payloadJSON),
	}
	return ev, nil
}

// GetEventsSince returns events with seq > sinceSeq, ordered ascending.
func (s *Store) GetEventsSince(sessionID string, sinceSeq int64, limit int) ([]protocol.EventPayload, error) {
	var rows *sql.Rows
	var err error
	if limit > 0 {
		rows, err = s.db.Query(
			`SELECT seq, ts, kind, payload_json FROM events WHERE session_id = ? AND seq > ? ORDER BY seq LIMIT ?`,
			sessionID, sinceSeq, limit,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT seq, ts, kind, payload_json FROM events WHERE session_id = ? AND seq > ? ORDER BY seq`,
			sessionID, sinceSeq,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

// GetSnapshot derives materialized state from the event log.
func (s *Store) GetSnapshot(sessionID string) (*Snapshot, error) {
	sess, err := s.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}

	rows, err := s.db.Query(
		`SELECT seq, ts, kind, payload_json FROM events WHERE session_id = ? ORDER BY seq`,
		sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events, err := scanEvents(rows)
	if err != nil {
		return nil, err
	}

	snap := &Snapshot{
		SessionID:          sessionID,
		Status:             sess.Status,
		LastSeq:            sess.LastSeq,
		PendingPermissions: []protocol.PermissionView{},
		PendingQuestions:   []protocol.QuestionView{},
	}
	pending := map[string]protocol.PermissionView{}
	pendingQ := map[string]protocol.QuestionView{}

	for _, ev := range events {
		snap.LastSeq = ev.Seq
		switch ev.Kind {
		case protocol.EventStatusChange:
			var body protocol.StatusChangeBody
			_ = json.Unmarshal(ev.Payload, &body)
			snap.Status = body.Status
		case protocol.EventModeChanged:
			var body protocol.ModeChangedBody
			_ = json.Unmarshal(ev.Payload, &body)
			snap.Mode = body.Mode
		case protocol.EventPermissionRequest:
			var body protocol.PermissionRequestBody
			_ = json.Unmarshal(ev.Payload, &body)
			pending[body.RequestID] = protocol.PermissionView{
				RequestID: body.RequestID,
				Kind:      body.Kind,
				Summary:   body.Summary,
			}
		case protocol.EventPermissionResolved:
			var body protocol.PermissionResolvedBody
			_ = json.Unmarshal(ev.Payload, &body)
			delete(pending, body.RequestID)
		case protocol.EventQuestion:
			var body protocol.QuestionBody
			_ = json.Unmarshal(ev.Payload, &body)
			pendingQ[body.QuestionID] = protocol.QuestionView{
				QuestionID: body.QuestionID,
				Text:       body.Text,
			}
		case protocol.EventQuestionResolved:
			var body protocol.QuestionResolvedBody
			_ = json.Unmarshal(ev.Payload, &body)
			delete(pendingQ, body.QuestionID)
		}
	}

	for _, v := range pending {
		snap.PendingPermissions = append(snap.PendingPermissions, v)
	}
	for _, v := range pendingQ {
		snap.PendingQuestions = append(snap.PendingQuestions, v)
	}
	return snap, nil
}

func scanEvents(rows *sql.Rows) ([]protocol.EventPayload, error) {
	var out []protocol.EventPayload
	for rows.Next() {
		var ev protocol.EventPayload
		var raw []byte
		var kind string
		err := rows.Scan(&ev.Seq, &ev.TS, &kind, &raw)
		if err != nil {
			return nil, err
		}
		ev.Kind = protocol.EventKind(kind)
		ev.Payload = json.RawMessage(raw)
		out = append(out, ev)
	}
	return out, rows.Err()
}

// Device methods.

func (s *Store) AddDevice(deviceID, name, credential string) error {
	hash := hashCredential(credential)
	now := time.Now().UnixMilli()
	_, err := s.db.Exec(
		`INSERT INTO devices (device_id, name, credential_hash, paired_at, revoked) VALUES (?, ?, ?, ?, 0)
		 ON CONFLICT(device_id) DO UPDATE SET name = excluded.name, credential_hash = excluded.credential_hash, revoked = 0`,
		deviceID, name, hash, now,
	)
	return err
}

func (s *Store) GetDevice(deviceID string) (*Device, error) {
	row := s.db.QueryRow(`SELECT device_id, name, credential_hash, paired_at, last_seen, revoked FROM devices WHERE device_id = ?`, deviceID)
	return scanDevice(row)
}

func (s *Store) GetDeviceByCredential(credential string) (*Device, error) {
	hash := hashCredential(credential)
	row := s.db.QueryRow(`SELECT device_id, name, credential_hash, paired_at, last_seen, revoked FROM devices WHERE credential_hash = ? AND revoked = 0`, hash)
	return scanDevice(row)
}

func (s *Store) ListDevices() ([]*Device, error) {
	rows, err := s.db.Query(`SELECT device_id, name, credential_hash, paired_at, last_seen, revoked FROM devices ORDER BY paired_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) RevokeDevice(deviceID string) error {
	_, err := s.db.Exec(`UPDATE devices SET revoked = 1 WHERE device_id = ?`, deviceID)
	return err
}

func (s *Store) UpdateDeviceLastSeen(deviceID string) error {
	now := time.Now().UnixMilli()
	_, err := s.db.Exec(`UPDATE devices SET last_seen = ? WHERE device_id = ?`, now, deviceID)
	return err
}

func scanDevice(s interface {
	Scan(dest ...interface{}) error
}) (*Device, error) {
	var d Device
	var paired, lastSeen sql.NullInt64
	var revoked int
	err := s.Scan(&d.DeviceID, &d.Name, &d.CredentialHash, &paired, &lastSeen, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d.PairedAt = time.UnixMilli(paired.Int64)
	if lastSeen.Valid {
		t := time.UnixMilli(lastSeen.Int64)
		d.LastSeen = &t
	}
	d.Revoked = revoked != 0
	return &d, nil
}

// PendingToken represents a short-lived, one-time device-pairing token.
type PendingToken struct {
	Token     string
	Name      string
	Approved  bool
	ExpiresAt time.Time
}

func (s *Store) CreatePendingToken(token string, expiresAt time.Time) error {
	_, err := s.db.Exec(
		`INSERT INTO pending_tokens (token, name, approved, expires_at) VALUES (?, '', 0, ?)`,
		token, expiresAt.UnixMilli(),
	)
	return err
}

func (s *Store) GetPendingToken(token string) (*PendingToken, error) {
	row := s.db.QueryRow(`SELECT token, name, approved, expires_at FROM pending_tokens WHERE token = ?`, token)
	return scanPendingToken(row)
}

func (s *Store) ApprovePendingToken(token, name string) error {
	res, err := s.db.Exec(`UPDATE pending_tokens SET name = ?, approved = 1 WHERE token = ?`, name, token)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("token not found")
	}
	return nil
}

// ConsumePendingToken validates and deletes an approved, non-expired token.
func (s *Store) ConsumePendingToken(token string) (*PendingToken, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	pt, err := scanPendingToken(tx.QueryRow(`SELECT token, name, approved, expires_at FROM pending_tokens WHERE token = ?`, token))
	if err != nil {
		return nil, err
	}
	if pt == nil {
		return nil, nil
	}
	if time.Now().After(pt.ExpiresAt) {
		_, _ = tx.Exec(`DELETE FROM pending_tokens WHERE token = ?`, token)
		_ = tx.Commit()
		return nil, nil
	}
	if !pt.Approved {
		return nil, nil
	}
	_, err = tx.Exec(`DELETE FROM pending_tokens WHERE token = ?`, token)
	if err != nil {
		return nil, err
	}
	return pt, tx.Commit()
}

func scanPendingToken(s interface {
	Scan(dest ...interface{}) error
}) (*PendingToken, error) {
	var pt PendingToken
	var expires int64
	var approved int
	err := s.Scan(&pt.Token, &pt.Name, &approved, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	pt.Approved = approved != 0
	pt.ExpiresAt = time.UnixMilli(expires)
	return &pt, nil
}

func randomID(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func hashCredential(credential string) string {
	h := sha256.Sum256([]byte(credential))
	return hex.EncodeToString(h[:])
}
