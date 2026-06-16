// Package agent also exposes helpers for resolving the public Platform
// bootstrap node. The defaults below can be overridden at runtime through the
// AGENT_PLATFORM_URL and AGENT_PLATFORM_PEER_ID environment variables.
package agent

import (
	"fmt"
	"os"
	"strings"
)

// DefaultPlatformHTTPURL is the canonical HTTPS base URL of the public
// agent-comm Platform. The Platform exposes a JSON HTTP API at this address
// for URN registration and bootstrap peer ID discovery.
const DefaultPlatformHTTPURL = "https://agent-communication.online"

// DefaultPlatformDomain is the bare host portion used when constructing P2P
// multiaddrs. We use the DNS form (multiaddr's /dns4/) so that libp2p can do
// runtime DNS resolution, with cached IPs from agent.DNSCache used as a
// fallback when DNS is unavailable.
const DefaultPlatformDomain = "agent-communication.online"

// DefaultPlatformP2PPort is the libp2p listen port exposed by the Platform.
// Both QUIC and TCP listeners bind here.
const DefaultPlatformP2PPort = 45041

// DefaultPlatformPeerID is the libp2p PeerID of the well-known Platform
// bootstrap node. It is used as a last-resort fallback when the HTTP
// bootstrap API is unreachable; production deployments should treat the HTTP
// API as the source of truth.
const DefaultPlatformPeerID = "12D3KooWKjNBA3pgLKryRytwHpJ9dPQo9H3gvCKUekktYtXQXfib"

// PlatformConfig is the runtime configuration for reaching the public
// agent-comm Platform. Fields are populated from environment variables when
// present, otherwise they fall back to the DefaultPlatform* constants above.
type PlatformConfig struct {
	// HTTPBaseURL is the HTTPS base URL for the Platform's REST API.
	HTTPBaseURL string

	// Domain is the bare hostname used to construct P2P multiaddrs.
	Domain string

	// P2PPort is the libp2p port the Platform listens on for QUIC and TCP.
	P2PPort int

	// PeerID is the libp2p PeerID of the Platform bootstrap node.
	PeerID string
}

// LoadPlatformConfig builds a PlatformConfig from environment variables with
// fallback to the DefaultPlatform* constants. The recognised variables are:
//
//   - AGENT_PLATFORM_URL      overrides DefaultPlatformHTTPURL
//   - AGENT_PLATFORM_DOMAIN   overrides DefaultPlatformDomain (defaults to
//     the hostname portion of HTTPBaseURL when not set)
//   - AGENT_PLATFORM_PEER_ID  overrides DefaultPlatformPeerID
//   - AGENT_PLATFORM_PORT     overrides DefaultPlatformP2PPort (integer)
func LoadPlatformConfig() PlatformConfig {
	cfg := PlatformConfig{
		HTTPBaseURL: DefaultPlatformHTTPURL,
		Domain:      DefaultPlatformDomain,
		P2PPort:     DefaultPlatformP2PPort,
		PeerID:      DefaultPlatformPeerID,
	}

	if v := strings.TrimSpace(os.Getenv("AGENT_PLATFORM_URL")); v != "" {
		cfg.HTTPBaseURL = strings.TrimRight(v, "/")
	}

	if v := strings.TrimSpace(os.Getenv("AGENT_PLATFORM_DOMAIN")); v != "" {
		cfg.Domain = v
	} else if u := strings.TrimSpace(cfg.HTTPBaseURL); u != "" {
		// Best-effort: extract hostname from the HTTP base URL. We import only
		// stdlib here to avoid pulling net/url just for one parse.
		if host := extractHost(u); host != "" {
			cfg.Domain = host
		}
	}

	if v := strings.TrimSpace(os.Getenv("AGENT_PLATFORM_PEER_ID")); v != "" {
		cfg.PeerID = v
	}

	if v := strings.TrimSpace(os.Getenv("AGENT_PLATFORM_PORT")); v != "" {
		var port int
		if _, err := fmt.Sscanf(v, "%d", &port); err == nil && port > 0 && port < 65536 {
			cfg.P2PPort = port
		}
	}

	return cfg
}

// extractHost returns the hostname portion of a URL string without using
// net/url. It accepts both "https://host:port" and "host:port" forms and is
// tolerant of trailing slashes. This keeps the PlatformConfig helpers free of
// additional imports for callers that only need a quick best-effort split.
func extractHost(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	// Strip scheme.
	if idx := strings.Index(s, "://"); idx >= 0 {
		s = s[idx+3:]
	}
	// Strip path.
	if idx := strings.Index(s, "/"); idx >= 0 {
		s = s[:idx]
	}
	// Strip userinfo.
	if idx := strings.Index(s, "@"); idx >= 0 {
		s = s[idx+1:]
	}
	// Strip port.
	if strings.HasPrefix(s, "[") {
		// IPv6 literal: [::1]:8080 -> ::1
		if end := strings.Index(s, "]"); end >= 0 {
			return s[1:end]
		}
	}
	if idx := strings.LastIndex(s, ":"); idx >= 0 {
		// Only strip if the suffix looks like a port number.
		suffix := s[idx+1:]
		allDigits := suffix != ""
		for _, c := range suffix {
			if c < '0' || c > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			return s[:idx]
		}
	}
	return s
}