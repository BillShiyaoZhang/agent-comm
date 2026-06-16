package agent

import "github.com/libp2p/go-libp2p/core/peer"

// Config defines the setup parameters for the Agent.
type Config struct {
	KeysDir        string
	DBPath         string // Path to the SQLite DB for Double Ratchet & Contacts
	ListenAddrs    []string
	EnableRelay    bool
	BootstrapNodes []peer.AddrInfo

	// URNPrefix overrides the namespace prefix used to derive the agent's
	// self-URN. An empty string means "use crypto.DefaultURNPrefix" which is
	// framework-agnostic ("urn:agent-comm:agent"). Set this if you need a
	// custom namespace, e.g. for migration from a previous prefix.
	URNPrefix string
}
