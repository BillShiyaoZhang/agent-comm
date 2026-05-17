// Package wot provides Web of Trust: signed trust claims and transitive trust path resolution.
package wot

import (
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nousresearch/hermes-agent/agent-comm/crypto"
	"github.com/nousresearch/hermes-agent/agent-comm/proto"
	_ "modernc.org/sqlite"
)

// Store persists trust claims in a local SQLite database.
// It is safe for concurrent use.
type Store struct {
	db   *sql.DB
	mu   sync.RWMutex
	keys *crypto.IdentityKeys // used for verifying claims when adding
}

// NewStore opens (or creates) the SQLite database at dbPath.
func NewStore(dbPath string, keys *crypto.IdentityKeys) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	// Enable WAL mode for better concurrent read performance
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return nil, fmt.Errorf("pragma WAL: %w", err)
	}

	s := &Store{db: db, keys: keys}
	if err := s.initSchema(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS claims (
		id          TEXT PRIMARY KEY,
		issuer_urn  TEXT NOT NULL,
		subject_urn TEXT NOT NULL,
		peer_id     TEXT NOT NULL,
		x25519_pk   BLOB NOT NULL,
		level       INTEGER NOT NULL,
		signature   BLOB NOT NULL,
		issued_at   INTEGER NOT NULL,
		fetched_at  INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_subject ON claims(subject_urn);
	CREATE INDEX IF NOT EXISTS idx_issuer  ON claims(issuer_urn);
	CREATE TABLE IF NOT EXISTS known_peers (
		urn         TEXT PRIMARY KEY,
		peer_id     TEXT NOT NULL,
		x25519_pk   BLOB NOT NULL,
		ed25519_pk  BLOB NOT NULL,
		first_seen  INTEGER NOT NULL,
		last_seen   INTEGER NOT NULL
	);
	`
	_, err := s.db.Exec(schema)
	return err
}

// AddClaim verifies and stores a TrustClaim.
// Returns an error if the signature is invalid or the claim is already stored.
// Claim ID is SHA256(issuer||subject||issued_at).
func (s *Store) AddClaim(c *TrustClaim) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Verify the signature using the issuer's Ed25519 pubkey from our known peers
	issuerPK, err := s.GetPeerEd25519PK(c.IssuerUrn)
	if err != nil {
		return fmt.Errorf("cannot verify claim: issuer %s unknown: %w", c.IssuerUrn, err)
	}
	if err := c.Verify(issuerPK); err != nil {
		return fmt.Errorf("claim verify: %w", err)
	}

	// Deduplicate: check if already stored
	claimID := claimID(c.IssuerUrn, c.SubjectUrn, c.IssuedAtUnix)
	var existing string
	row := s.db.QueryRow("SELECT id FROM claims WHERE id = ?", claimID)
	if err := row.Scan(&existing); err == nil {
		return nil // already stored, skip
	}

	_, err = s.db.Exec(`
		INSERT INTO claims (id, issuer_urn, subject_urn, peer_id, x25519_pk, level, signature, issued_at, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		claimID, c.IssuerUrn, c.SubjectUrn, c.SubjectPeerId, c.SubjectX25519Pk,
		int(c.Level), c.IssuerSignature, c.IssuedAtUnix, nowUnix(),
	)
	return err
}

// GetClaimsAbout returns all claims about a given subject URN.
func (s *Store) GetClaimsAbout(subjectURN string) ([]*TrustClaim, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT issuer_urn, subject_urn, peer_id, x25519_pk, level, signature, issued_at
		FROM claims WHERE subject_urn = ?`, subjectURN)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanClaims(rows)
}

// GetClaimsBy returns all claims issued by a given issuer URN.
func (s *Store) GetClaimsBy(issuerURN string) ([]*TrustClaim, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT issuer_urn, subject_urn, peer_id, x25519_pk, level, signature, issued_at
		FROM claims WHERE issuer_urn = ?`, issuerURN)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanClaims(rows)
}

// GetAllClaims returns all claims in the store.
func (s *Store) GetAllClaims() ([]*TrustClaim, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT issuer_urn, subject_urn, peer_id, x25519_pk, level, signature, issued_at
		FROM claims`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanClaims(rows)
}

// AddKnownPeer records a peer's public keys (X25519 and Ed25519).
// Used for verifying signatures on claims from that peer.
func (s *Store) AddKnownPeer(urn, peerID string, x25519PK, ed25519PK []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(x25519PK) != 32 {
		return fmt.Errorf("X25519 pubkey must be 32 bytes")
	}
	if len(ed25519PK) != ed25519.PublicKeySize {
		return fmt.Errorf("Ed25519 pubkey must be %d bytes", ed25519.PublicKeySize)
	}

	now := nowUnix()
	_, err := s.db.Exec(`
		INSERT INTO known_peers (urn, peer_id, x25519_pk, ed25519_pk, first_seen, last_seen)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(urn) DO UPDATE SET peer_id=excluded.peer_id, x25519_pk=excluded.x25519_pk, ed25519_pk=excluded.ed25519_pk, last_seen=excluded.last_seen`,
		urn, peerID, x25519PK, ed25519PK, now, now)
	return err
}

// GetKnownPeer returns the public keys for a known peer URN.
func (s *Store) GetKnownPeer(urn string) (peerID string, x25519PK, ed25519PK []byte, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRow("SELECT peer_id, x25519_pk, ed25519_pk FROM known_peers WHERE urn = ?", urn)
	err = row.Scan(&peerID, &x25519PK, &ed25519PK)
	if err == sql.ErrNoRows {
		err = fmt.Errorf("peer %s unknown", urn)
	}
	return
}

// GetPeerEd25519PK returns only the Ed25519 pubkey for a known peer.
func (s *Store) GetPeerEd25519PK(urn string) ([]byte, error) {
	_, _, ed25519PK, err := s.GetKnownPeer(urn)
	return ed25519PK, err
}

// GetPeerX25519PK returns only the X25519 pubkey for a known peer.
func (s *Store) GetPeerX25519PK(urn string) ([]byte, error) {
	_, x25519PK, _, err := s.GetKnownPeer(urn)
	return x25519PK, err
}

// AddMyClaim stores a claim made BY this node (issuer = our URN).
// Skips signature verification (we signed it ourselves).
func (s *Store) AddMyClaim(c *TrustClaim) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	claimID := claimID(c.IssuerUrn, c.SubjectUrn, c.IssuedAtUnix)
	_, err := s.db.Exec(`
		INSERT OR IGNORE INTO claims (id, issuer_urn, subject_urn, peer_id, x25519_pk, level, signature, issued_at, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		claimID, c.IssuerUrn, c.SubjectUrn, c.SubjectPeerId, c.SubjectX25519Pk,
		int(c.Level), c.IssuerSignature, c.IssuedAtUnix, nowUnix(),
	)
	return err
}

// ListTrustedPeers returns URNs that this node has explicitly marked as TRUSTED.
func (s *Store) ListTrustedPeers() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query("SELECT DISTINCT subject_urn FROM claims WHERE level = ?", int(proto.TrustLevel_TRUSTED))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var urn string
		if err := rows.Scan(&urn); err == nil {
			out = append(out, urn)
		}
	}
	return out, rows.Err()
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// ---- helpers ----

func scanClaims(rows *sql.Rows) ([]*TrustClaim, error) {
	var out []*TrustClaim
	for rows.Next() {
		var c proto.TrustClaim
		var level int
		if err := rows.Scan(&c.IssuerUrn, &c.SubjectUrn, &c.SubjectPeerId, &c.SubjectX25519Pk, &level, &c.IssuerSignature, &c.IssuedAtUnix); err != nil {
			return nil, err
		}
		c.Level = proto.TrustLevel(level)
		out = append(out, &TrustClaim{TrustClaim: &c})
	}
	return out, rows.Err()
}

func claimID(issuer, subject string, issuedAt int64) string {
	h := sha256.Sum256([]byte(issuer + subject + fmt.Sprintf("%d", issuedAt)))
	return fmt.Sprintf("%x", h[:16])
}

func nowUnix() int64 { return time.Now().Unix() }