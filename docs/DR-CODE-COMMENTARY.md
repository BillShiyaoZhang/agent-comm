// Code Commentary — dr/ Package (Double Ratchet / Signal Protocol)
//
// This package implements the Double Ratchet Algorithm (Signal Protocol) for
// forward-secret encrypted sessions between agents. It builds on the session.Manager's
// ECIES key agreement but handles all subsequent messages via the Double Ratchet,
// ensuring that compromise of one message key does not expose past or future messages.
//
// ─────────────────────────────────────────────────────────────────────────────
// ARCHITECTURE OVERVIEW
// ─────────────────────────────────────────────────────────────────────────────
//
// Three files divide responsibilities:
//
//   dr/store.go    — SQLite persistence for RatchetState (per-peer session state)
//   dr/ratchet.go  — Core Double Ratchet algorithm (DH ratchet + symmetric ratchet)
//   dr/session.go  — libp2p stream transport wrapper (DRSession)
//
// The typical flow:
//
//   Alice (initiator)                         Bob (responder)
//   ─────────────────                         ─────────────────
//   1. ECDH(X25519_SK, Bob_PK)          →     (Bob knows Alice's static PK)
//      → shared secret (seed)
//   2. InitAlice(sharedSecret)          →     (ratchet uninitialized)
//   3. Send() → DR message              →     Receive() on stream
//                                            InitBobWithSS() + ReceiveMsg1()
//                                            FinishRatchet() after decrypt
//   4. ... subsequent messages ...
//
// ─────────────────────────────────────────────────────────────────────────────
// dr/store.go — RatchetState Persistence (SQLite)
// ─────────────────────────────────────────────────────────────────────────────
//
// DRStore wraps an SQLite database (modernc.org/sqlite) and maps peer URNs to
// serialized RatchetState blobs. WAL mode is enabled for concurrent reads.
//
// Schema:
//
//   CREATE TABLE dr_sessions (
//     peer_urn  TEXT PRIMARY KEY,   -- peer's URN (e.g. urn:hermes:agent:...)
//     peer_id   TEXT NOT NULL,       -- libp2p peer ID string
//     state     BLOB NOT NULL,       -- RatchetState.Serialize() bytes (234 bytes)
//     updated_at INTEGER NOT NULL    -- Unix timestamp
//   )
//
// Key methods:
//
//   SaveSession(peerURN, peerID, state) — upsert; uses SQLite ON CONFLICT DO UPDATE
//                                         so saves are idempotent per peer URN
//   LoadSession(peerURN)                — returns (state, found, error); found=false
//                                         means no prior session (first contact)
//   DeleteSession(peerURN)              — removes session on logout or reset
//   Close()                             — releases the DB connection
//
// The 234-byte serialized state layout (from ratchet.go Serialize):
//   [0:32]   DHSecret
//   [32:64]  DHPub
//   [64:96]  TheirDHPub
//   [96:128] RootKey
//   [128:160] origRootKey
//   [160:192] SendChainKey
//   [192:224] ReceiveChainKey
//   [224:228] SendCount   (big-endian uint32)
//   [228:232] RecvCount   (big-endian uint32)
//   [232]     Initialized  (0 or 1)
//   [233]     firstDH      (0 or 1)
//
// The store is thread-safe via a sync.RWMutex (read-lock Load, write-lock Save/Delete).
//
// ─────────────────────────────────────────────────────────────────────────────
// dr/ratchet.go — Double Ratchet Algorithm
// ─────────────────────────────────────────────────────────────────────────────
//
// RatchetState is the central data structure. It is stateful — one per peer session.
// All fields are unexported; access is only through the methods defined in the file.
//
// ── Key Fields ────────────────────────────────────────────────────────────────
//
//   DHSecret / DHPub       Our current X25519 key pair (rotated on each DH ratchet)
//   TheirDHPub             The peer's current DH public key (from message header)
//   RootKey [32]           HKDF master key from which chain keys are derived
//   origRootKey [32]      Root key before the first DH ratchet. Used as the
//                         base for both recv and send chains during the symmetric
//                         ratchet step (Signal spec "symmetric ratchet").
//   SendChainKey / SendCount    Outbound chain: each message advances the chain and
//                               increments SendCount. ChaCha20-Poly1305 key.
//   ReceiveChainKey / RecvCount Inbound chain: same structure, separate counters.
//   Initialized bool           Whether the ratchet has been bootstrapped.
//   firstDH bool               Tracks whether the first DH ratchet step has occurred.
//                              During firstDH=true, origRootKey is used as the
//                              KDF base for both chains (symmetric ratchet).
//
// ── Initialization ─────────────────────────────────────────────────────────────
//
// Alice (initiator):
//   InitAlice(dhOutput) — called with ECDH(X25519_SK, peer_PK).
//   Steps:
//     1. GenerateDHKey() → new X25519 keypair (A1_SK, A1_PK)
//     2. kdfRootChain(dhOutput, zero, nil) → RootKey, SendChainKey
//     3. origRootKey = RootKey  (saved for symmetric ratchet)
//     4. firstDH = true, Initialized = true
//
// Bob (responder):
//   InitBobWithSS(staticSS) — called with ECDH(Bob_SK, Alice_PK).
//   Derives initial RootKey and ReceiveChainKey WITHOUT generating a DH keypair
//   or advancing the ratchet. The DH ratchet happens in FinishRatchet() AFTER
//   the first message is decrypted.
//   FinishRatchet(theirDHPub):
//     1. ECDH(B1_SK, A1_PK) → recv chain DH output
//     2. GenerateDHKey() → B2 keypair
//     3. recv chain = kdfRootChain(ECDH(B1_SK, A1_PK), origRootKey)
//     4. send chain = kdfRootChain(ECDH(B2_SK, A1_PK), origRootKey)  ← symmetric
//
// This ordering (decrypt first, then DH ratchet) is critical: Signal spec requires
// the receiver to defer the DH ratchet until AFTER processing the first message
// using the initial receive chain key.
//
// ── Send Flow ─────────────────────────────────────────────────────────────────
//
//   1. kdfMessageKey(SendChainKey) → msgKey, nextChainKey
//      kdfMessageKey uses HKDF-SHA256 to derive a 32-byte message key,
//      and HMAC-SHA256(chainKey, 0x01) for the next chain key (Signal spec).
//   2. Build DrHeader{DHPub: DHPub, PN: 0, MsgNum: SendCount}
//      DHPub is our current DH public key (changes after each DH ratchet).
//      PN (Previous Chain Length) is used for out-of-order message recovery.
//   3. encryptWithKey(msgKey, plaintext) → nonce || ciphertext  (ChaCha20-Poly1305 XAE)
//   4. Advance: SendChainKey = nextChainKey, SendCount++
//
// ── Receive Flow ──────────────────────────────────────────────────────────────
//
// Subsequent messages (after initialization):
//   Receive(m *DrMessage):
//     1. If m.Header.DHPub != TheirDHPub → dhRatchet(theirDHPub) first
//     2. Verify MsgNum == RecvCount (constant-time comparison via int check)
//     3. kdfMessageKey(ReceiveChainKey) → msgKey, nextChainKey
//     4. decryptWithKey(msgKey, ciphertext) → plaintext
//     5. Advance: ReceiveChainKey = nextChainKey, RecvCount++
//
// First message (Bob only):
//   ReceiveMsg1(msg):
//     1. Extract theirDHPub from msg[:32]
//     2. Verify MsgNum == RecvCount (0)
//     3. kdfMessageKey(ReceiveChainKey) → msgKey, nextChainKey
//     4. Decrypt msg[40:] (ciphertext after 40-byte header)
//     5. Advance ReceiveChainKey and RecvCount
//     6. Return (plaintext, theirDHPub) — caller invokes FinishRatchet
//
// ── DH Ratchet (dhRatchet) ───────────────────────────────────────────────────
//
// Triggered when the received message's DH public key differs from TheirDHPub.
// Signal spec symmetric + DH ratchet:
//
//   For first DH ratchet (firstDH=true):
//     rootKeyBase = origRootKey  (both chains use original seed)
//
//   For subsequent DH ratchets (firstDH=false):
//     rootKeyBase = current RootKey
//
//   Step 1: recv chain = kdfRootChain(ECDH(cur_SK, theirNewPub), rootKeyBase)
//   Step 2: GenerateDHKey() → new DH keypair (advances our side)
//   Step 3: send chain = kdfRootChain(ECDH(new_SK, theirNewPub), rootKeyBase)
//
// Due to curve25519 commutativity, ECDH(A_SK, B_PK) = ECDH(B_SK, A_PK),
// so both sides derive the same DH output for each chain.
//
// ── Key Derivation Functions ───────────────────────────────────────────────────
//
// kdfRootChain(dhOutput, currentRootKey, info):
//   IKM = currentRootKey || dhOutput  (64 bytes)
//   HKDF-SHA256 with info = "DoubleRatchet" || info
//   Output: [0:32] = new RootKey, [32:64] = new ChainKey
//
// kdfMessageKey(chainKey):
//   HKDF-SHA256(chainKey, nil, "DoubleRatchetMessage") → msgKey[0:32]
//   HMAC-SHA256(chainKey, 0x01) → nextChainKey[0:32]
//
// ── Serialization ──────────────────────────────────────────────────────────────
//
// Serialize() — 234-byte binary layout (documented above in store section).
// DeserializeRatchetState(data) — reverses the layout.
//
// SerializeHeader / DeserializeHeader — 40-byte header format:
//   [0:32]   DHPub   (sender's current DH public key)
//   [32:36]  PN      (previous chain length, big-endian)
//   [36:40]  MsgNum  (message number in current chain, big-endian)
//
// ─────────────────────────────────────────────────────────────────────────────
// dr/session.go — DRSession: libp2p Transport Wrapper
// ─────────────────────────────────────────────────────────────────────────────
//
// DRSession wraps RatchetState with libp2p stream I/O for use in the agent-comm
// protocol stack. It is instantiated by either NewDRSessionInitiator (Alice) or
// NewDRSessionResponder (Bob).
//
// Protocol ID: /agent/dr/1.0.0 (libp2p protocol negotiation)
//
// Message format on wire (length-prefixed):
//   [4 bytes: big-endian size][40 bytes: DR header][N bytes: ciphertext]
//
// ── DRSession Fields ───────────────────────────────────────────────────────────
//
//   peerURN  string        — peer's URN (used as store key)
//   peerID   peer.ID       — libp2p peer ID for stream dialing
//   manager  *session.Manager — provides Host(), Ecies(), PeerStaticX25519PK()
//   keys     *crypto.IdentityKeys — our identity keys (X25519_SK for ECDH)
//   ratchet  RatchetState  — the actual DR state (mutx-protected)
//   mu       sync.RWMutex  — protects ratchet field
//
// ── Initiator (Alice) Setup ───────────────────────────────────────────────────
//
// NewDRSessionInitiator(ctx, mgr, keys, peerID, peerX25519PK, peerURN):
//   1. Compute ECDH(X25519_SK, peerX25519PK) → sharedSecret  (session.Manager.Ecies)
//      This is the "IK" (Identity Key) agreement from X3DH (our ECIES bootstrap).
//   2. Hash sharedSecret → 32 bytes (HashSharedSecret = SHA-256)
//   3. ratchet.InitAlice(array32(seed)) → bootstraps the ratchet
//   4. Alice immediately has SendCount=0, RecvCount=0
//
// Alice's first Send() includes her initial DH public key in the header.
// Bob receives this, runs InitBobWithSS + ReceiveMsg1, then FinishRatchet.
//
// ── Responder (Bob) Setup ──────────────────────────────────────────────────────
//
// NewDRSessionResponder() — creates an uninitialized DRSession.
// The ratchet is only initialized when the first message arrives in Receive():
//   1. ECDH(Bob_SK, Alice_static_PK) → staticSS
//   2. ratchet.InitBobWithSS(staticSS)
//   3. ratchet.ReceiveMsg1(msgBytes) → plaintext, theirDHPub
//   4. ratchet.FinishRatchet(theirDHPub) → completes the symmetric ratchet
//
// The ordering (decrypt before DH ratchet) matches Signal spec requirement.
//
// ── Send() / Receive() Semantics ───────────────────────────────────────────────
//
// DRSession.Send(ctx, plaintext):
//   1. ratchet.Send(plaintext) → DrMessage{Hdr, Ct}
//   2. Serialize header (40 bytes) + append ciphertext
//   3. Open new libp2p stream to peerID with protocol ProtoID
//   4. Write: [4-byte size][msgBytes]
//   5. stream.CloseWrite() — half-close to signal message complete
//   6. Read encrypted response (DR is symmetric: every Send gets a Receive)
//   7. DeserializeResponse → ratchet.Receive() to advance the ratchet
//      and decrypt the reply (using now-advanced receive chain)
//   8. Return (response plaintext is not returned to caller in current impl)
//
// DRSession.Receive(ctx, stream):
//   1. Read [4-byte size][msgBytes] from stream
//   2. If not initialized: InitBobWithSS + ReceiveMsg1 + FinishRatchet
//   3. Otherwise: DeserializeHeader + ratchet.Receive()
//   4. Return plaintext payload
//
// ── Symmetric vs DH Ratchet ───────────────────────────────────────────────────
//
// Symmetric Ratchet (every message):
//   chain_key → message_key + next_chain_key
//   Used for both send and receive chains independently.
//   Provides forward secrecy: old message keys are erased when chain advances.
//
// DH Ratchet (on new peer DH public key):
//   DH output → new root key + new chain keys
//   Provides break-in recovery: compromising old keys doesn't expose future
//   keys unless the attacker also performs the DH ratchet with the compromised key.
//
// The combination means: forward secrecy + break-in recovery + message key unlinkability.
//
// ─────────────────────────────────────────────────────────────────────────────
// RELATIONSHIP TO session/session.go
// ─────────────────────────────────────────────────────────────────────────────
//
// session.Manager (session.go) provides:
//   - ECIES-only encryption for initial key agreement (static ECDH)
//   - EncryptedEnvelope protobuf with ephemeral DH key for each message
//   - Used for: contact discovery tokens, initial handshake, non-DR messages
//
// dr/ package replaces ECIES with Double Ratchet for:
//   - Long-running encrypted message sessions
//   - Any use case requiring forward secrecy and break-in recovery
//
// Integration:
//   - DRSession uses session.Manager only for Host(), Ecies(), and PeerStaticX25519PK()
//   - Once initialized, all encryption goes through RatchetState (ChaCha20-Poly1305)
//   - DRStore persists RatchetState across restarts
//   - Both use the same crypto.IdentityKeys for identity and X25519 static keys
//
// ─────────────────────────────────────────────────────────────────────────────
// SECURITY PROPERTIES
// ─────────────────────────────────────────────────────────────────────────────
//
// Forward Secrecy:       Each message uses a one-time message key derived from the
//                        chain key. Compromise of a message key does NOT expose
//                        past messages (chain key was already advanced).
//
// Break-in Recovery:     After a compromise, the next DH ratchet step produces
//                        a new DH keypair that the attacker doesn't know,
//                        restoring security for subsequent messages.
//
// Message Key Unlinkability: Each message key is cryptographically unlinkable to
//                        any other message key — ciphertexts cannot be attributed
//                        to the same chain without knowing the chain key.
//
// Constant-time Counters: Message number checks use direct integer comparison,
//                        not constant-time. For high-security contexts, consider
//                        adding constant-time comparison for MsgNum checks.
//
// ─────────────────────────────────────────────────────────────────────────────
// LIMITATIONS & NOTES
// ─────────────────────────────────────────────────────────────────────────────
//
// - Message numbers are not encrypted (PN and MsgNum are plaintext in header).
// - No out-of-order message buffering; RecvCount must match exactly.
// - Skipped message numbers (gap) cause decryption failure — suitable for
//   in-order transport (libp2p streams) but not for UDP/datagram use cases.
// - The firstDH flag is cleared after the first DH ratchet but not persisted.
//   AfterDeserialize, firstDH is assumed true until the next dhRatchet call.
// - RatchetState.Serialize() is NOT encrypted — store.go relies on filesystem
//   permissions. For untrusted storage, encrypt the serialized blob.