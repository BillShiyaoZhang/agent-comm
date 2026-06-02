package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/BillShiyaoZhang/agent-comm/crypto"
	pb "github.com/BillShiyaoZhang/agent-comm/proto"
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
		runDaemon()
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
	if len(os.Args) < 5 {
		printError("Usage: agent-comm-helper encrypt-envelope <keys_dir> <recipient_pubkey_hex> <plaintext_hex>")
		os.Exit(1)
	}
	keysDir := os.Args[2]
	recipientPubkeyHex := os.Args[3]
	plaintextHex := os.Args[4]

	recipientPubKey, err := hex.DecodeString(recipientPubkeyHex)
	if err != nil || len(recipientPubKey) != 32 {
		printError("invalid recipient X25519 public key (must be 32 bytes hex)")
		os.Exit(1)
	}

	plaintextBytes, err := hex.DecodeString(plaintextHex)
	if err != nil {
		printError("invalid plaintext hex")
		os.Exit(1)
	}

	keys, err := crypto.LoadOrCreateIdentity(keysDir)
	if err != nil {
		printError(fmt.Sprintf("load keys failed: %v", err))
		os.Exit(1)
	}

	msg := &pb.ChatMessage{
		Body: &pb.ChatMessage_Text{
			Text: &pb.TextMessage{
				Text:      string(plaintextBytes),
				Timestamp: time.Now().UnixMilli(),
			},
		},
	}
	payload, err := goproto.Marshal(msg)
	if err != nil {
		printError(fmt.Sprintf("protobuf marshal failed: %v", err))
		os.Exit(1)
	}

	ecies := crypto.NewECIES()
	sharedSecret, err := ecies.ComputeSharedSecret(keys.X25519SK, recipientPubKey)
	if err != nil {
		printError(fmt.Sprintf("ECDH shared secret computation failed: %v", err))
		os.Exit(1)
	}

	aad := sha256.Sum256([]byte("agent-comm-v1"))
	ephemeral, nonce, ciphertext, tag, err := ecies.EncryptWithSharedSecret(sharedSecret, payload, aad[:16])
	if err != nil {
		printError(fmt.Sprintf("encryption failed: %v", err))
		os.Exit(1)
	}

	messageID := crypto.GenerateMessageID()

	printResult(Response{
		"sender_urn":           keys.Ed25519.URN(),
		"sender_static_pubkey": hex.EncodeToString(keys.X25519PK),
		"ephemeral_pubkey":     hex.EncodeToString(ephemeral),
		"nonce":                hex.EncodeToString(nonce),
		"ciphertext":           hex.EncodeToString(ciphertext),
		"tag":                  hex.EncodeToString(tag),
		"message_id":           messageID,
	})
}

func runDecryptEnvelope() {
	if len(os.Args) < 8 {
		printError("Usage: agent-comm-helper decrypt-envelope <keys_dir> <sender_static_pubkey_hex> <ephemeral_pubkey_hex> <nonce_hex> <ciphertext_hex> <tag_hex>")
		os.Exit(1)
	}
	keysDir := os.Args[2]
	senderStaticPubkeyHex := os.Args[3]
	ephemeralPubkeyHex := os.Args[4]
	nonceHex := os.Args[5]
	ciphertextHex := os.Args[6]
	tagHex := os.Args[7]

	senderStaticPubkey, err := hex.DecodeString(senderStaticPubkeyHex)
	if err != nil || len(senderStaticPubkey) != 32 {
		printError("invalid sender static pubkey")
		os.Exit(1)
	}
	ephemeralPubkey, err := hex.DecodeString(ephemeralPubkeyHex)
	if err != nil || len(ephemeralPubkey) != 32 {
		printError("invalid ephemeral pubkey")
		os.Exit(1)
	}
	nonce, err := hex.DecodeString(nonceHex)
	if err != nil {
		printError("invalid nonce")
		os.Exit(1)
	}
	ciphertext, err := hex.DecodeString(ciphertextHex)
	if err != nil {
		printError("invalid ciphertext")
		os.Exit(1)
	}
	tag, err := hex.DecodeString(tagHex)
	if err != nil {
		printError("invalid tag")
		os.Exit(1)
	}

	keys, err := crypto.LoadOrCreateIdentity(keysDir)
	if err != nil {
		printError(fmt.Sprintf("load keys failed: %v", err))
		os.Exit(1)
	}

	ecies := crypto.NewECIES()
	sharedSecret, err := ecies.ComputeSharedSecret(keys.X25519SK, senderStaticPubkey)
	if err != nil {
		printError(fmt.Sprintf("ECDH shared secret failed: %v", err))
		os.Exit(1)
	}

	aad := sha256.Sum256([]byte("agent-comm-v1"))
	plaintextBytes, err := ecies.DecryptWithSharedSecret(
		sharedSecret, ephemeralPubkey, nonce, ciphertext, tag, aad[:16])
	if err != nil {
		printError(fmt.Sprintf("ECIES decryption failed: %v", err))
		os.Exit(1)
	}

	var chatMsg pb.ChatMessage
	if err := goproto.Unmarshal(plaintextBytes, &chatMsg); err != nil {
		printError(fmt.Sprintf("protobuf unmarshal failed: %v", err))
		os.Exit(1)
	}

	text := ""
	if txt := chatMsg.GetText(); txt != nil {
		text = txt.Text
	}

	printResult(Response{
		"plaintext": text,
	})
}
