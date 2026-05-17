// Package contacts provides local contact management with trusted pubkey cache.
package contacts

import (
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Contact represents a known peer with their verified public keys.
// A contact may be "trusted" (explicitly vouched for via WoT) or "known" (seen but not trusted).
type Contact struct {
	URN         string    // "urn:hermes:agent:Alice"
	PeerID      string    // libp2p PeerID
	X25519PK    []byte    // 32 bytes, for ECIES encryption
	Ed25519PK   []byte    // 32 bytes, for verifying WoT claims
	DisplayName string    // friendly name
	Trusted     bool      // explicitly trusted (via WoT or manual bootstrap)
	FirstSeen   time.Time
	LastSeen    time.Time
}

// Store manages the local contact list backed by SQLite.
type Store struct {
	db *sql.DB
	mu sync.RWMutex
}

// NewStore opens (or creates) the contacts database at dbPath.
func NewStore(dbPath string) (*Store, error) {
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
	s := &Store{db: db}
	if err := s.initSchema(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) initSchema() error {
	_, err := s.db.Exec(`
	CREATE TABLE IF NOT EXISTS contacts (
		urn           TEXT PRIMARY KEY,
		peer_id       TEXT NOT NULL,
		x25519_pk     BLOB NOT NULL,
		ed25519_pk    BLOB NOT NULL,
		display_name  TEXT DEFAULT '',
		trusted       INTEGER NOT NULL DEFAULT 0,
		first_seen    INTEGER NOT NULL,
		last_seen     INTEGER NOT NULL
	);
	CREATE TABLE IF NOT EXISTS trusted_urns (
		urn     TEXT PRIMARY KEY,
		comment TEXT DEFAULT '',
		added   INTEGER NOT NULL
	);
	`)
	return err
}

// Add adds or updates a contact.
func (s *Store) Add(c *Contact) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(c.X25519PK) != 32 || len(c.Ed25519PK) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid key length")
	}

	now := time.Now().Unix()
	trusted := 0
	if c.Trusted {
		trusted = 1
	}

	_, err := s.db.Exec(`
	INSERT INTO contacts (urn, peer_id, x25519_pk, ed25519_pk, display_name, trusted, first_seen, last_seen)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(urn) DO UPDATE SET
		peer_id=excluded.peer_id, x25519_pk=excluded.x25519_pk, ed25519_pk=excluded.ed25519_pk,
		display_name=excluded.display_name, trusted=excluded.trusted, last_seen=excluded.last_seen`,
		c.URN, c.PeerID, c.X25519PK, c.Ed25519PK, c.DisplayName, trusted, now, now)
	return err
}

// Get returns a contact by URN.
func (s *Store) Get(urn string) (*Contact, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRow(`
		SELECT urn, peer_id, x25519_pk, ed25519_pk, display_name, trusted, first_seen, last_seen
		FROM contacts WHERE urn = ?`, urn)

	var c Contact
	var trusted int
	var firstSeen, lastSeen int64
	err := row.Scan(&c.URN, &c.PeerID, &c.X25519PK, &c.Ed25519PK, &c.DisplayName, &trusted, &firstSeen, &lastSeen)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("contact not found: %s", urn)
	}
	if err != nil {
		return nil, err
	}
	c.Trusted = trusted == 1
	c.FirstSeen = time.Unix(firstSeen, 0)
	c.LastSeen = time.Unix(lastSeen, 0)
	return &c, nil
}

// GetPubkeys returns both public keys for a contact.
func (s *Store) GetPubkeys(urn string) (x25519, ed25519 []byte, err error) {
	c, err := s.Get(urn)
	if err != nil {
		return nil, nil, err
	}
	return c.X25519PK, c.Ed25519PK, nil
}

// IsTrusted returns true if the contact is marked as trusted.
func (s *Store) IsTrusted(urn string) bool {
	c, err := s.Get(urn)
	if err != nil {
		return false
	}
	return c.Trusted
}

// SetTrusted marks or unmarks a contact as trusted.
func (s *Store) SetTrusted(urn string, trusted bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	trustedVal := 0
	if trusted {
		trustedVal = 1
		// Also record in trusted_urns table for WoT integration
		now := time.Now().Unix()
		s.db.Exec(`INSERT OR IGNORE INTO trusted_urns (urn, added) VALUES (?, ?)`, urn, now)
	} else {
		s.db.Exec(`DELETE FROM trusted_urns WHERE urn = ?`, urn)
	}

	_, err := s.db.Exec(`UPDATE contacts SET trusted = ? WHERE urn = ?`, trustedVal, urn)
	return err
}

// List returns all contacts.
func (s *Store) List() ([]*Contact, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT urn, peer_id, x25519_pk, ed25519_pk, display_name, trusted, first_seen, last_seen
		FROM contacts ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Contact
	for rows.Next() {
		var c Contact
		var trusted int
		var firstSeen, lastSeen int64
		if err := rows.Scan(&c.URN, &c.PeerID, &c.X25519PK, &c.Ed25519PK, &c.DisplayName, &trusted, &firstSeen, &lastSeen); err != nil {
			return nil, err
		}
		c.Trusted = trusted == 1
		c.FirstSeen = time.Unix(firstSeen, 0)
		c.LastSeen = time.Unix(lastSeen, 0)
		out = append(out, &c)
	}
	return out, rows.Err()
}

// ListTrusted returns all trusted contacts.
func (s *Store) ListTrusted() ([]*Contact, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT urn, peer_id, x25519_pk, ed25519_pk, display_name, trusted, first_seen, last_seen
		FROM contacts WHERE trusted = 1 ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Contact
	for rows.Next() {
		var c Contact
		var trusted int
		var firstSeen, lastSeen int64
		if err := rows.Scan(&c.URN, &c.PeerID, &c.X25519PK, &c.Ed25519PK, &c.DisplayName, &trusted, &firstSeen, &lastSeen); err != nil {
			return nil, err
		}
		c.Trusted = true
		c.FirstSeen = time.Unix(firstSeen, 0)
		c.LastSeen = time.Unix(lastSeen, 0)
		out = append(out, &c)
	}
	return out, rows.Err()
}

// Remove deletes a contact.
func (s *Store) Remove(urn string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`DELETE FROM contacts WHERE urn = ?`, urn)
	return err
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Fingerprint returns a short hex fingerprint (SHA256 of X25519 PK[:16]).
func Fingerprint(pk []byte) string {
	if len(pk) < 16 {
		return ""
	}
	h := sha256.Sum256(pk)
	return fmt.Sprintf("%x", h[:8])
}