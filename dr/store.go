// Package dr implements the Double Ratchet (Signal Protocol) for forward-secret encrypted sessions.
package dr

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// DRStore persists Double Ratchet session state in SQLite.
type DRStore struct {
	db *sql.DB
	mu sync.RWMutex
}

// NewDRStore opens (or creates) the SQLite database at dbPath.
func NewDRStore(dbPath string) (*DRStore, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return nil, fmt.Errorf("pragma WAL: %w", err)
	}

	s := &DRStore{db: db}
	if err := s.initSchema(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *DRStore) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS dr_sessions (
		peer_urn     TEXT PRIMARY KEY,
		peer_id      TEXT NOT NULL,
		state        BLOB NOT NULL,
		updated_at   INTEGER NOT NULL
	);
	`
	_, err := s.db.Exec(schema)
	return err
}

// SaveSession stores or updates the ratchet state for a peer URN.
func (s *DRStore) SaveSession(peerURN, peerID string, state *RatchetState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	serialized := state.Serialize()
	now := timestamp()
	_, err := s.db.Exec(`
		INSERT INTO dr_sessions (peer_urn, peer_id, state, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(peer_urn) DO UPDATE SET peer_id=excluded.peer_id, state=excluded.state, updated_at=excluded.updated_at`,
		peerURN, peerID, serialized, now)
	return err
}

// LoadSession loads the ratchet state for a peer URN.
// Returns (state, true, nil) on success, (nil, false, nil) if not found.
func (s *DRStore) LoadSession(peerURN string) (*RatchetState, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRow("SELECT state FROM dr_sessions WHERE peer_urn = ?", peerURN)
	var data []byte
	if err := row.Scan(&data); err == sql.ErrNoRows {
		return nil, false, nil
	} else if err != nil {
		return nil, false, err
	}

	state, err := DeserializeRatchetState(data)
	if err != nil {
		return nil, false, err
	}
	return &state, true, nil
}

// DeleteSession removes the stored ratchet state for a peer URN.
func (s *DRStore) DeleteSession(peerURN string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec("DELETE FROM dr_sessions WHERE peer_urn = ?", peerURN)
	return err
}

// Close closes the database connection.
func (s *DRStore) Close() error { return s.db.Close() }

func timestamp() int64 { return time.Now().Unix() }