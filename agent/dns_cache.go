package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// dnsCacheFileName is the file name (within the cache directory) where the
// DNSCache persists its entries between daemon restarts.
const dnsCacheFileName = "dns_cache.json"

// DefaultDNSCacheTTL is the time a successful DNS resolution is considered
// fresh. Once the TTL expires, the next LookupHost re-queries DNS.
const DefaultDNSCacheTTL = time.Hour

// dnsEntry is one cached resolution result. IPs is sorted so the JSON form is
// stable for diffing, and ExpireAt is a Unix timestamp (seconds).
type dnsEntry struct {
	IPs      []string `json:"ips"`
	ExpireAt int64    `json:"expire_at"`
}

// DNSCache memoises net.DefaultResolver.LookupHost results to disk so that
// libp2p can fall back to /ip4/<cached>/... multiaddrs when DNS lookups fail.
//
// The cache lives in the OS-appropriate user cache directory
// (os.UserCacheDir()) by default. Callers that need a different location
// (typically tests) can pass an explicit cacheDir to NewDNSCache.
type DNSCache struct {
	mu    sync.RWMutex
	items map[string]dnsEntry

	cacheDir string
	ttl      time.Duration

	// lookup is a hook for tests to stub DNS resolution. In production this
	// always points at net.DefaultResolver.LookupHost.
	lookup func(ctx context.Context, host string) ([]string, error)
}

// NewDNSCache returns a cache rooted in cacheDir. If cacheDir is empty, the
// directory is os.UserCacheDir()/agent-comm. The cache is loaded eagerly from
// disk if a previous run wrote entries; missing or corrupt files are treated
// as an empty cache rather than an error.
func NewDNSCache(cacheDir string) (*DNSCache, error) {
	if cacheDir == "" {
		base, err := os.UserCacheDir()
		if err != nil || base == "" {
			base = os.TempDir()
		}
		cacheDir = filepath.Join(base, "agent-comm")
	}
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create dns cache dir: %w", err)
	}

	c := &DNSCache{
		items:    make(map[string]dnsEntry),
		cacheDir: cacheDir,
		ttl:      DefaultDNSCacheTTL,
		lookup:   func(ctx context.Context, host string) ([]string, error) {
			ips, err := net.DefaultResolver.LookupHost(ctx, host)
			if err != nil {
				return nil, err
			}
			// Filter out non-IP strings (LookupHost can return CNAMEs in some
			// edge cases); we only want literal IPv4/IPv6 addresses.
			out := make([]string, 0, len(ips))
			for _, ip := range ips {
				if parsed := net.ParseIP(ip); parsed != nil {
					out = append(out, ip)
				}
			}
			return out, nil
		},
	}
	_ = c.load()
	return c, nil
}

// CacheDir returns the directory where the cache persists its JSON file.
func (c *DNSCache) CacheDir() string {
	return c.cacheDir
}

// SetTTL overrides the cache time-to-live. The default is DefaultDNSCacheTTL.
func (c *DNSCache) SetTTL(ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	c.mu.Lock()
	c.ttl = ttl
	c.mu.Unlock()
}

// LookupHost returns the IPs currently associated with host, performing a
// fresh DNS query if no entry exists or the cached entry has expired. The
// returned slice is freshly allocated; callers may mutate it freely.
func (c *DNSCache) LookupHost(ctx context.Context, host string) ([]string, error) {
	if host == "" {
		return nil, fmt.Errorf("empty host")
	}

	if ips, ok := c.freshEntry(host); ok {
		// Return a copy so callers can sort/mutate without poisoning the cache.
		out := make([]string, len(ips))
		copy(out, ips)
		return out, nil
	}

	ips, err := c.lookup(ctx, host)
	if err != nil {
		// DNS failure: try to serve the stale cache as a last resort. If the
		// cache itself is empty, surface the original error.
		if stale, ok := c.anyEntry(host); ok {
			out := make([]string, len(stale))
			copy(out, stale)
			return out, nil
		}
		return nil, err
	}

	sort.Strings(ips)
	c.mu.Lock()
	c.items[host] = dnsEntry{
		IPs:      append([]string(nil), ips...),
		ExpireAt: time.Now().Add(c.ttl).Unix(),
	}
	c.mu.Unlock()
	_ = c.persist()

	out := make([]string, len(ips))
	copy(out, ips)
	return out, nil
}

// ResolveWithFallback returns both a /dns4/<host>/... host string and one
// /ip4/<ip>/... string per resolved IP. The DNS form is preferred because
// libp2p can re-resolve on its own and will pick the freshest path; the IP
// forms are useful as fallbacks when DNS is flaky.
//
// The returned DNS string and IP strings do not include any transport or
// peer-id suffix; callers are expected to append /udp/<port>/quic-v1 or
// /tcp/<port> and /p2p/<peerID> themselves.
func (c *DNSCache) ResolveWithFallback(ctx context.Context, host string) (dns string, ips []string, err error) {
	ips, err = c.LookupHost(ctx, host)
	if err != nil {
		// Best-effort: even if DNS failed, expose the host form so libp2p can
		// try a /dns4/... lookup itself with its own resolver.
		return host, nil, err
	}
	return host, ips, nil
}

// Forget clears the cached entry for host. Subsequent LookupHost calls will
// re-query DNS. This is mainly useful for tests and recovery flows.
func (c *DNSCache) Forget(host string) {
	c.mu.Lock()
	delete(c.items, host)
	c.mu.Unlock()
	_ = c.persist()
}

// freshEntry returns the cached IPs for host when the entry has not expired.
func (c *DNSCache) freshEntry(host string) ([]string, bool) {
	c.mu.RLock()
	entry, ok := c.items[host]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if time.Now().Unix() >= entry.ExpireAt {
		return nil, false
	}
	return entry.IPs, true
}

// anyEntry returns the cached IPs for host regardless of expiry. This is used
// as a last-resort fallback when DNS queries fail.
func (c *DNSCache) anyEntry(host string) ([]string, bool) {
	c.mu.RLock()
	entry, ok := c.items[host]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return entry.IPs, true
}

// load reads the JSON cache file from disk. A missing or corrupt file yields
// an empty cache and no error.
func (c *DNSCache) load() error {
	path := c.filePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var items map[string]dnsEntry
	if err := json.Unmarshal(data, &items); err != nil {
		return err
	}
	c.mu.Lock()
	c.items = items
	c.mu.Unlock()
	return nil
}

// persist writes the in-memory cache to disk atomically via a temp file rename.
// Failures are non-fatal for callers; the in-memory copy remains valid.
func (c *DNSCache) persist() error {
	c.mu.RLock()
	snapshot := make(map[string]dnsEntry, len(c.items))
	for k, v := range c.items {
		snapshot[k] = v
	}
	c.mu.RUnlock()

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	final := c.filePath()
	tmp, err := os.CreateTemp(filepath.Dir(final), "dns_cache-*.json.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		// If we never renamed, clean up the stray tmp file.
		_ = os.Remove(tmpName)
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, final)
}

func (c *DNSCache) filePath() string {
	return filepath.Join(c.cacheDir, dnsCacheFileName)
}