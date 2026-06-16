package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// stubLookup lets a test inject a canned DNS response (or failure) into the
// cache without actually contacting the resolver.
func stubLookup(t *testing.T, c *DNSCache, ips []string, err error) {
	t.Helper()
	c.mu.Lock()
	c.lookup = func(ctx context.Context, host string) ([]string, error) {
		return ips, err
	}
	c.mu.Unlock()
}

func TestDNSCache_MissThenHit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	c, err := NewDNSCache(dir)
	if err != nil {
		t.Fatalf("NewDNSCache: %v", err)
	}
	stubLookup(t, c, []string{"1.1.1.1", "9.9.9.9"}, nil)

	ips, err := c.LookupHost(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("LookupHost: %v", err)
	}
	sort.Strings(ips)
	if len(ips) != 2 || ips[0] != "1.1.1.1" || ips[1] != "9.9.9.9" {
		t.Fatalf("unexpected ips: %v", ips)
	}

	// Second call should be served from cache; replace stub with a failing
	// lookup and confirm the cached entry is returned.
	stubLookup(t, c, nil, errors.New("dns down"))
	ips2, err := c.LookupHost(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("LookupHost cached: %v", err)
	}
	if len(ips2) != 2 {
		t.Fatalf("cached ips wrong length: %v", ips2)
	}
}

func TestDNSCache_TTLExpiry(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	c, err := NewDNSCache(dir)
	if err != nil {
		t.Fatalf("NewDNSCache: %v", err)
	}
	c.SetTTL(50 * time.Millisecond)

	stubLookup(t, c, []string{"1.1.1.1"}, nil)
	if _, err := c.LookupHost(context.Background(), "example.com"); err != nil {
		t.Fatalf("first lookup: %v", err)
	}

	// Replace stub so we can detect a re-query.
	stubLookup(t, c, []string{"2.2.2.2"}, nil)
	time.Sleep(80 * time.Millisecond)

	ips, err := c.LookupHost(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("second lookup: %v", err)
	}
	if len(ips) != 1 || ips[0] != "2.2.2.2" {
		t.Fatalf("expected refreshed 2.2.2.2, got %v", ips)
	}
}

func TestDNSCache_DNSFailureServesStale(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	c, err := NewDNSCache(dir)
	if err != nil {
		t.Fatalf("NewDNSCache: %v", err)
	}
	stubLookup(t, c, []string{"3.3.3.3"}, nil)
	if _, err := c.LookupHost(context.Background(), "example.com"); err != nil {
		t.Fatalf("warm cache: %v", err)
	}

	// Force expiry so freshEntry() would refuse, but stubLookup fails to
	// simulate a real DNS outage. The cache should still return the stale IPs
	// because anyEntry() ignores expiry.
	c.SetTTL(10 * time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	stubLookup(t, c, nil, errors.New("resolver down"))

	ips, err := c.LookupHost(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("expected stale fallback to succeed: %v", err)
	}
	if len(ips) != 1 || ips[0] != "3.3.3.3" {
		t.Fatalf("expected stale 3.3.3.3, got %v", ips)
	}
}

func TestDNSCache_Persistence(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")

	c1, err := NewDNSCache(dir)
	if err != nil {
		t.Fatalf("NewDNSCache #1: %v", err)
	}
	stubLookup(t, c1, []string{"4.4.4.4"}, nil)
	if _, err := c1.LookupHost(context.Background(), "example.com"); err != nil {
		t.Fatalf("populate: %v", err)
	}

	// Ensure file exists on disk.
	path := filepath.Join(dir, dnsCacheFileName)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected cache file at %s: %v", path, err)
	}

	// Re-open the cache from the same directory and ensure it loads.
	c2, err := NewDNSCache(dir)
	if err != nil {
		t.Fatalf("NewDNSCache #2: %v", err)
	}
	stubLookup(t, c2, nil, errors.New("no network"))

	ips, err := c2.LookupHost(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("expected persisted entry to load: %v", err)
	}
	if len(ips) != 1 || ips[0] != "4.4.4.4" {
		t.Fatalf("persisted entry mismatch: %v", ips)
	}
}

func TestExtractHost(t *testing.T) {
	cases := map[string]string{
		"https://agent-communication.online":       "agent-communication.online",
		"https://agent-communication.online:443":  "agent-communication.online",
		"http://1.2.3.4:8080":                     "1.2.3.4",
		"agent-communication.online":              "agent-communication.online",
		"https://user:pw@host.example.com:9000/x": "host.example.com",
		"[::1]:8080":                              "::1",
		"https://[2001:db8::1]:443/":              "2001:db8::1",
	}
	for in, want := range cases {
		if got := extractHost(in); got != want {
			t.Errorf("extractHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoadPlatformConfig_EnvOverrides(t *testing.T) {
	t.Setenv("AGENT_PLATFORM_URL", "https://staging.example.org")
	t.Setenv("AGENT_PLATFORM_PEER_ID", "12D3KooWSTAGING")
	t.Setenv("AGENT_PLATFORM_PORT", "4711")

	cfg := LoadPlatformConfig()
	if cfg.HTTPBaseURL != "https://staging.example.org" {
		t.Errorf("HTTPBaseURL = %q", cfg.HTTPBaseURL)
	}
	if cfg.PeerID != "12D3KooWSTAGING" {
		t.Errorf("PeerID = %q", cfg.PeerID)
	}
	if cfg.P2PPort != 4711 {
		t.Errorf("P2PPort = %d", cfg.P2PPort)
	}
	// Domain falls back to the hostname derived from HTTPBaseURL when not set.
	if cfg.Domain != "staging.example.org" {
		t.Errorf("Domain = %q", cfg.Domain)
	}
}

func TestLoadPlatformConfig_Defaults(t *testing.T) {
	// Make sure the env vars don't leak from other tests.
	t.Setenv("AGENT_PLATFORM_URL", "")
	t.Setenv("AGENT_PLATFORM_DOMAIN", "")
	t.Setenv("AGENT_PLATFORM_PEER_ID", "")
	t.Setenv("AGENT_PLATFORM_PORT", "")

	cfg := LoadPlatformConfig()
	if cfg.HTTPBaseURL != DefaultPlatformHTTPURL {
		t.Errorf("HTTPBaseURL default = %q", cfg.HTTPBaseURL)
	}
	if cfg.Domain != DefaultPlatformDomain {
		t.Errorf("Domain default = %q", cfg.Domain)
	}
	if cfg.PeerID != DefaultPlatformPeerID {
		t.Errorf("PeerID default = %q", cfg.PeerID)
	}
	if cfg.P2PPort != DefaultPlatformP2PPort {
		t.Errorf("P2PPort default = %d", cfg.P2PPort)
	}
}