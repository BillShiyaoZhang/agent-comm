package main

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/BillShiyaoZhang/agent-comm/crypto"
	pb "github.com/BillShiyaoZhang/agent-comm/proto"
	"github.com/BillShiyaoZhang/agent-comm/session"
	goproto "google.golang.org/protobuf/proto"
)

type Response map[string]interface{}

func main() {
	if len(os.Args) < 2 {
		printError("missing command. Usage: agent-comm-helper <command> [args...]")
		os.Exit(1)
	}

	cmd := os.Args[1]
	switch cmd {
	case "init":
		runInit()
	case "sign-retrieve":
		runSignRetrieve()
	case "sign-store":
		runSignStore()
	case "encrypt-envelope":
		runEncryptEnvelope()
	case "decrypt-envelope":
		runDecryptEnvelope()
	case "daemon":
		if err := runDaemon(); err != nil {
			printError(err.Error())
			os.Exit(1)
		}
	default:
		printError(fmt.Sprintf("unknown command: %s", cmd))
		os.Exit(1)
	}
}

func printError(msg string) {
	resp := Response{
		"error": msg,
	}
	bytes, _ := json.Marshal(resp)
	fmt.Println(string(bytes))
}

func printResult(res Response) {
	bytes, err := json.Marshal(res)
	if err != nil {
		printError(fmt.Sprintf("JSON marshal error: %v", err))
		return
	}
	fmt.Println(string(bytes))
}

func runInit() {
	if len(os.Args) < 3 {
		printError("Usage: agent-comm-helper init <keys_dir>")
		os.Exit(1)
	}
	keysDir := os.Args[2]
	keys, err := crypto.LoadOrCreateIdentity(keysDir)
	if err != nil {
		printError(fmt.Sprintf("load/create identity failed: %v", err))
		os.Exit(1)
	}

	peerID, err := keys.PeerID()
	if err != nil {
		printError(fmt.Sprintf("failed to get PeerID: %v", err))
		os.Exit(1)
	}

	printResult(Response{
		"urn":            keys.Ed25519.URN(),
		"peer_id":        peerID,
		"ed25519_pubkey": hex.EncodeToString(keys.Ed25519.PublicKey),
		"x25519_pubkey":  hex.EncodeToString(keys.X25519PK),
	})
}

func runSignRetrieve() {
	if len(os.Args) < 5 {
		printError("Usage: agent-comm-helper sign-retrieve <keys_dir> <urn> <timestamp>")
		os.Exit(1)
	}
	keysDir := os.Args[2]
	urn := os.Args[3]
	tsStr := os.Args[4]

	timestamp, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		printError(fmt.Sprintf("invalid timestamp: %v", err))
		os.Exit(1)
	}

	keys, err := crypto.LoadOrCreateIdentity(keysDir)
	if err != nil {
		printError(fmt.Sprintf("load keys failed: %v", err))
		os.Exit(1)
	}

	tsBuf := make([]byte, 8)
	binary.BigEndian.PutUint64(tsBuf, uint64(timestamp))
	msg := append([]byte("mq-retrieve|"+urn+"|"), tsBuf...)

	sig, err := keys.Ed25519.Sign(msg)
	if err != nil {
		printError(fmt.Sprintf("signing failed: %v", err))
		os.Exit(1)
	}

	printResult(Response{
		"signature": hex.EncodeToString(sig),
		"pubkey":    hex.EncodeToString(keys.Ed25519.PublicKey),
	})
}

func runSignStore() {
	if len(os.Args) < 4 {
		printError("Usage: agent-comm-helper sign-store <keys_dir> <body_hex>")
		os.Exit(1)
	}
	keysDir := os.Args[2]
	bodyHex := os.Args[3]

	bodyBytes, err := hex.DecodeString(bodyHex)
	if err != nil {
		printError(fmt.Sprintf("invalid body hex: %v", err))
		os.Exit(1)
	}

	keys, err := crypto.LoadOrCreateIdentity(keysDir)
	if err != nil {
		printError(fmt.Sprintf("load keys failed: %v", err))
		os.Exit(1)
	}

	sig, err := keys.Ed25519.Sign(bodyBytes)
	if err != nil {
		printError(fmt.Sprintf("signing failed: %v", err))
		os.Exit(1)
	}

	printResult(Response{
		"signature": hex.EncodeToString(sig),
		"pubkey":    hex.EncodeToString(keys.Ed25519.PublicKey),
	})
}

func runEncryptEnvelope() {
	if len(os.Args) != 7 {
		printError("authenticated envelopes require a recipient URN and stable message ID. Usage: agent-comm-helper encrypt-envelope <keys_dir> <recipient_urn> <recipient_pubkey_hex> <plaintext_hex> <message_id>")
		os.Exit(1)
	}
	recipientPubKey, err := hex.DecodeString(os.Args[4])
	if err != nil || len(recipientPubKey) != 32 {
		printError("invalid recipient X25519 public key (must be 32 bytes hex)")
		os.Exit(1)
	}
	plaintext, err := hex.DecodeString(os.Args[5])
	if err != nil {
		printError("invalid plaintext hex")
		os.Exit(1)
	}
	keys, err := crypto.LoadOrCreateIdentity(os.Args[2])
	if err != nil {
		printError(fmt.Sprintf("load keys failed: %v", err))
		os.Exit(1)
	}
	envelope, err := session.NewManager(nil, keys).BuildEnvelopeForRecipient(os.Args[3], recipientPubKey, string(plaintext), os.Args[6])
	if err != nil {
		printError(fmt.Sprintf("encrypt envelope: %v", err))
		os.Exit(1)
	}
	wire, err := goproto.Marshal(envelope)
	if err != nil {
		printError(fmt.Sprintf("marshal envelope: %v", err))
		os.Exit(1)
	}
	printResult(Response{
		"sender_urn":            envelope.SenderUrn,
		"recipient_urn":         envelope.RecipientUrn,
		"sender_static_pubkey":  hex.EncodeToString(envelope.SenderStaticPubkey),
		"sender_ed25519_pubkey": hex.EncodeToString(envelope.SenderEd25519Pubkey),
		"ephemeral_pubkey":      hex.EncodeToString(envelope.EphemeralPubkey),
		"nonce":                 hex.EncodeToString(envelope.Nonce),
		"ciphertext":            hex.EncodeToString(envelope.Ciphertext),
		"tag":                   hex.EncodeToString(envelope.Tag),
		"message_id":            envelope.MessageId,
		"signature":             hex.EncodeToString(envelope.Signature),
		"envelope_proto_hex":    hex.EncodeToString(wire),
	})
}

func runDecryptEnvelope() {
	if len(os.Args) != 4 {
		printError("unsigned field-only envelopes are no longer accepted. Usage: agent-comm-helper decrypt-envelope <keys_dir> <envelope_proto_hex>")
		os.Exit(1)
	}
	wire, err := hex.DecodeString(os.Args[3])
	if err != nil || len(wire) > crypto.MaxEnvelopeSize {
		printError("invalid or oversized envelope protobuf hex")
		os.Exit(1)
	}
	var envelope pb.EncryptedEnvelope
	if err := goproto.Unmarshal(wire, &envelope); err != nil {
		printError(fmt.Sprintf("unmarshal envelope: %v", err))
		os.Exit(1)
	}
	keys, err := crypto.LoadOrCreateIdentity(os.Args[2])
	if err != nil {
		printError(fmt.Sprintf("load keys failed: %v", err))
		os.Exit(1)
	}
	payload, err := session.NewManager(nil, keys).DecryptEnvelope(&envelope)
	if err != nil {
		printError(fmt.Sprintf("authenticate/decrypt envelope: %v", err))
		os.Exit(1)
	}
	var msg pb.ChatMessage
	if err := goproto.Unmarshal(payload, &msg); err != nil {
		printError(fmt.Sprintf("unmarshal message: %v", err))
		os.Exit(1)
	}
	text := ""
	if txt := msg.GetText(); txt != nil {
		text = txt.Text
	}
	printResult(Response{"plaintext": text, "sender_urn": envelope.SenderUrn, "recipient_urn": envelope.RecipientUrn, "message_id": envelope.MessageId})
}
