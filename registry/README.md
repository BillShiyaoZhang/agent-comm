# Registry ownership

Every new registration and update must pass `ValidateRegistration`. The URN
fingerprint and the canonical libp2p PeerID must identify the supplied Ed25519
public key. Custom URN namespaces are supported. The X25519 and Ed25519 public
keys must each be 32 bytes, and the Ed25519 signature must be 64 bytes.

Sign the bytes returned by `BuildSignedMsg(urn, peerID, x25519PK,
storesUserData, timestamp)` with the URN owner's Ed25519 private key. The
timestamp is Unix seconds and must be positive and between `now - 300` and
`now + 60`, inclusive. This prevents an unsigned or attacker-signed registration
from claiming an absent URN or replacing its owner's record.

`InMemoryStore.RegisterWithSignature` validates direct writes, and the P2P server
validates requests before forwarding them to any custom `Store`. Custom stores
should also call `ValidateRegistration` in their own write method so that direct
callers receive the same protection. The old unsigned `Client.Register`,
`InMemoryStore.Register`, and `HandleRegister` methods reject registration. Both
`HandleRegister` methods now return an error; migrate local bootstrap writes to
the store's `RegisterWithSignature` and handle its error.

`VerifyRegistration` validates persisted record ownership and signature without
requiring a recent timestamp. Use it for stored records, with expiration enforced
by the store's own lifetime policy. `VerifyResolveResult` uses this verification
and rejects missing signatures. Records admitted before signature enforcement
must pass the same identity checks before being returned or used.

P2P registration supports publication by a third party holding a valid,
recent owner-signed record. The stream's remote peer is the publisher and need
not be the record owner. Re-publication within the admission window is allowed;
the timestamp check does not impose monotonic updates or one-time nonces.

The existing signature authenticates the URN, PeerID, X25519 key, storage policy
and timestamp. It does **not** authenticate `addrs` or `relay_addrs`. These remain
untrusted routing hints; a publisher can change them, and connections must
authenticate the expected peer identity. Authenticating these addresses would
require a new version of the signed message format.
