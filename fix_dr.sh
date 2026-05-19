#!/bin/bash
sed -i '' -e '14i\
	"github.com/nousresearch/hermes-agent/agent-comm/dr"\
' agent/agent.go

sed -i '' -e '/BootstrapNodes \[\]peer.AddrInfo/a\
	DRStore        *dr.DRStore\
' agent/agent.go
