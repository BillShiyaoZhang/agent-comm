package agent

import (
	"context"
	"testing"
)

func TestDefaultAgentDoesNotJoinVulnerableDHT(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := InitIdentity(ctx, Config{KeysDir: t.TempDir(), ListenAddrs: []string{"/ip4/127.0.0.1/tcp/0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.DHT != nil {
		t.Fatal("default agent automatically enabled experimental DHT discovery")
	}
	for _, protocol := range a.Host.Mux().Protocols() {
		if protocol == "/ipfs/kad/1.0.0" {
			t.Fatal("default agent accepts Kademlia streams")
		}
	}
}
