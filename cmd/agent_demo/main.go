package main

import (
	"context"
	"fmt"
	"os"

	"github.com/nousresearch/hermes-agent/agent-comm/agent"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

func main() {
	var bootstrapNodes []peer.AddrInfo

	bootstrapAddr := os.Getenv("BOOTSTRAP_PEER")
	if bootstrapAddr != "" {
		maddr, err := multiaddr.NewMultiaddr(bootstrapAddr)
		if err == nil {
			info, err := peer.AddrInfoFromP2pAddr(maddr)
			if err == nil {
				bootstrapNodes = append(bootstrapNodes, *info)
			}
		}
	}

	cfg := agent.Config{
		KeysDir:        "./demo_keys",
		DBPath:         "./demo_dr.db",
		ListenAddrs:    []string{"/ip4/0.0.0.0/tcp/0", "/ip4/0.0.0.0/udp/0/quic"},
		EnableRelay:    true,
		BootstrapNodes: bootstrapNodes,
	}

	ctx := context.Background()
	a, err := agent.InitIdentity(ctx, cfg)
	if err != nil {
		fmt.Printf("Failed to init agent: %v\n", err)
		return
	}
	defer a.Host.Close()

	fmt.Printf("Started Agent successfully.\nURN: %s\nPeerID: %s\n", a.Keys.Ed25519.URN(), a.Host.ID())

	// Start receiving
	a.OnMessage(ctx, func(senderURN string, msg string) {
		fmt.Printf("\n<<< Received from %s: %s\n", senderURN, msg)
	})

	if len(os.Args) > 2 && os.Args[1] == "send" {
		targetURN := os.Args[2]
		text := "Hello via Hybrid P2P SDK!"
		if len(os.Args) > 3 {
			text = os.Args[3]
		}
		
		err := a.SendMessage(ctx, targetURN, text)
		if err != nil {
			fmt.Printf("Send failed: %v\n", err)
		}
	}

	select {}
}
