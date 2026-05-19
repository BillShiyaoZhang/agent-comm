package agent

import "github.com/libp2p/go-libp2p/core/peer"

// Config defines the setup parameters for the Agent.
type Config struct {
	KeysDir        string
	DBPath         string // Path to the SQLite DB for Double Ratchet & Contacts
	ListenAddrs    []string
	EnableRelay    bool
	BootstrapNodes []peer.AddrInfo
}
