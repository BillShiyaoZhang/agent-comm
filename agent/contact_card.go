package agent

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/mr-tron/base58"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/multiformats/go-multiaddr"
	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/nousresearch/hermes-agent/agent-comm/contacts"
	"github.com/nousresearch/hermes-agent/agent-comm/crypto"
	"github.com/nousresearch/hermes-agent/agent-comm/session"
)

// ContactCardInfo holds the parsed fields from an agent-comm contact card.
type ContactCardInfo struct {
	Ed25519PK      []byte
	X25519PK       []byte
	Addrs          []multiaddr.Multiaddr
	BootstrapAddrs []peer.AddrInfo
}

// GenerateContactCard generates a text message containing the agent's connection card.
func GenerateContactCard(keys *crypto.IdentityKeys, h host.Host, bootstrapNodes []peer.AddrInfo) (string, error) {
	// Gather self-addresses
	var addrs []string
	for _, addr := range h.Addrs() {
		// Only share listen addresses that are dialable (exclude loopback if desired, but for local testing let's include all)
		addrs = append(addrs, addr.String())
	}

	// Gather bootstrap/relay nodes
	var bootstrapAddrs []string
	for _, node := range bootstrapNodes {
		for _, addr := range node.Addrs {
			// Combine into a single /p2p/ multiaddress for ease of dialing/bootstrapping
			fullAddr := fmt.Sprintf("%s/p2p/%s", addr.String(), node.ID.String())
			bootstrapAddrs = append(bootstrapAddrs, fullAddr)
		}
	}

	cardBuilder := strings.Builder{}
	cardBuilder.WriteString("👋 Hello! Here is my AI Agent's communication card. You can use this card to connect with my agent via the agent-comm P2P secure protocol.\n\n")
	cardBuilder.WriteString("If you haven't installed the agent-comm skill yet, please clone and set up the repository first:\n")
	cardBuilder.WriteString("GitHub Repository: https://github.com/BillShiyaoZhang/agent-comm\n\n")
	cardBuilder.WriteString("To add me as a contact, copy the entire block below and import it via your agent command line or import API:\n\n")
	cardBuilder.WriteString("-----BEGIN AGENT-COMM CONTACT CARD-----\n")
	cardBuilder.WriteString(fmt.Sprintf("Ed25519PK: %x\n", keys.Ed25519.PublicKey))
	cardBuilder.WriteString(fmt.Sprintf("X25519PK: %x\n", keys.X25519PK))
	if len(addrs) > 0 {
		cardBuilder.WriteString(fmt.Sprintf("Addrs: %s\n", strings.Join(addrs, ",")))
	}
	if len(bootstrapAddrs) > 0 {
		cardBuilder.WriteString(fmt.Sprintf("Bootstrap: %s\n", strings.Join(bootstrapAddrs, ",")))
	}
	cardBuilder.WriteString("-----END AGENT-COMM CONTACT CARD-----\n")

	return cardBuilder.String(), nil
}

// ParseContactCard extracts and parses the ContactCardInfo from a raw contact card text block.
func ParseContactCard(text string) (*ContactCardInfo, error) {
	scanner := bufio.NewScanner(strings.NewReader(text))
	inCard := false
	var ed25519Hex, x25519Hex string
	var addrsStr, bootstrapStr string

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "-----BEGIN AGENT-COMM CONTACT CARD-----" {
			inCard = true
			continue
		}
		if line == "-----END AGENT-COMM CONTACT CARD-----" {
			inCard = false
			break
		}
		if !inCard {
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		switch key {
		case "Ed25519PK":
			ed25519Hex = val
		case "X25519PK":
			x25519Hex = val
		case "Addrs":
			addrsStr = val
		case "Bootstrap":
			bootstrapStr = val
		}
	}

	if ed25519Hex == "" || x25519Hex == "" {
		return nil, errors.New("missing critical fields in contact card (Ed25519PK and X25519PK are required)")
	}

	ed25519PK, err := hex.DecodeString(ed25519Hex)
	if err != nil || len(ed25519PK) != 32 {
		return nil, fmt.Errorf("invalid Ed25519PK in contact card: must be 32 bytes hex")
	}

	x25519PK, err := hex.DecodeString(x25519Hex)
	if err != nil || len(x25519PK) != 32 {
		return nil, fmt.Errorf("invalid X25519PK in contact card: must be 32 bytes hex")
	}

	var addrs []multiaddr.Multiaddr
	if addrsStr != "" {
		for _, aStr := range strings.Split(addrsStr, ",") {
			aStr = strings.TrimSpace(aStr)
			if aStr == "" {
				continue
			}
			ma, err := multiaddr.NewMultiaddr(aStr)
			if err != nil {
				continue
			}
			addrs = append(addrs, ma)
		}
	}

	var bootstrapNodes []peer.AddrInfo
	if bootstrapStr != "" {
		for _, bStr := range strings.Split(bootstrapStr, ",") {
			bStr = strings.TrimSpace(bStr)
			if bStr == "" {
				continue
			}
			ma, err := multiaddr.NewMultiaddr(bStr)
			if err != nil {
				continue
			}
			info, err := peer.AddrInfoFromP2pAddr(ma)
			if err != nil {
				continue
			}
			bootstrapNodes = append(bootstrapNodes, *info)
		}
	}

	return &ContactCardInfo{
		Ed25519PK:      ed25519PK,
		X25519PK:       x25519PK,
		Addrs:          addrs,
		BootstrapAddrs: bootstrapNodes,
	}, nil
}

// ImportContactCard imports the contact card details into host, session, and contacts store.
func ImportContactCard(h host.Host, mgr *session.Manager, store *contacts.Store, cardText string, displayName string) (*contacts.Contact, error) {
	info, err := ParseContactCard(cardText)
	if err != nil {
		return nil, fmt.Errorf("failed to parse contact card: %w", err)
	}

	// Derive URN and PeerID from Ed25519PK
	urn := URNFromEd25519PK(info.Ed25519PK)

	pid, err := PeerIDFromEd25519PK(info.Ed25519PK)
	if err != nil {
		return nil, fmt.Errorf("failed to derive PeerID: %w", err)
	}

	// 1. Add addresses to peerstore
	if len(info.Addrs) > 0 {
		h.Peerstore().AddAddrs(pid, info.Addrs, peerstore.PermanentAddrTTL)
	}

	// 2. Cache X25519 PK in session manager
	mgr.SetPeerX25519PK(pid, info.X25519PK)

	// 3. Add to contact store if initialized
	var contact *contacts.Contact
	if store != nil {
		contact = &contacts.Contact{
			URN:         urn,
			PeerID:      pid.String(),
			X25519PK:    info.X25519PK,
			Ed25519PK:   info.Ed25519PK,
			DisplayName: displayName,
			Trusted:     true, // Explicitly imported, so trust
		}
		if err := store.Add(contact); err != nil {
			return nil, fmt.Errorf("failed to add contact to database: %w", err)
		}
	}

	return contact, nil
}

// URNFromEd25519PK derives the URN string from an Ed25519 public key.
func URNFromEd25519PK(pubKey []byte) string {
	h := sha256.Sum256(pubKey)
	fingerprint := base58.Encode(h[:16])
	return fmt.Sprintf("urn:hermes:agent:%s", fingerprint)
}

// PeerIDFromEd25519PK derives a libp2p peer.ID from an Ed25519 public key.
func PeerIDFromEd25519PK(pubKey []byte) (peer.ID, error) {
	libp2pPub, err := libp2pcrypto.UnmarshalEd25519PublicKey(pubKey)
	if err != nil {
		return "", err
	}
	return peer.IDFromPublicKey(libp2pPub)
}

// GenerateContactCard generates a text card for this agent.
func (a *Agent) GenerateContactCard() (string, error) {
	return GenerateContactCard(a.Keys, a.Host, a.BootstrapNodes)
}

// ImportContactCard imports a card and adds it as a contact.
func (a *Agent) ImportContactCard(cardText string, displayName string) (*contacts.Contact, error) {
	return ImportContactCard(a.Host, a.Session, a.Contacts, cardText, displayName)
}
