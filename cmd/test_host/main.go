package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/nousresearch/hermes-agent/agent-comm/libp2p"
)

func main() {
	fmt.Println("=== agent-comm Phase 1: libp2p Host Test ===\n")

	cfg := libp2p.DefaultConfig()
	fmt.Printf("Config: %+v\n\n", cfg)

	h, err := libp2p.NewHost(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to create host: %v\n", err)
		os.Exit(1)
	}
	defer h.Close()

	fmt.Printf("✅ Host created successfully!\n")
	fmt.Printf("   PeerID : %s\n", libp2p.GetPeerID(h))
	fmt.Printf("   Addrs   : %v\n\n", h.Addrs())

	// Graceful shutdown
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	fmt.Println("👋 Listening... Press Ctrl+C to exit.")
	<-sig

	fmt.Println("\n✅ Shutdown clean")
}