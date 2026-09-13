package mq

import (
	"context"
	"crypto/ed25519"
	"fmt"

	"github.com/BillShiyaoZhang/agent-comm/crypto"
)

type authenticatedKeyContext struct{}

// WithAuthenticatedPublicKey carries an identity already authenticated by the
// transport (an HTTP signature or a libp2p secure connection). It must never be
// populated from an unverified client field.
func WithAuthenticatedPublicKey(ctx context.Context, publicKey []byte) context.Context {
	return context.WithValue(ctx, authenticatedKeyContext{}, append([]byte(nil), publicKey...))
}

// AuthorizeRecipient enforces mailbox ownership at the shared storage boundary.
func AuthorizeRecipient(ctx context.Context, recipientURN string) error {
	publicKey, _ := ctx.Value(authenticatedKeyContext{}).([]byte)
	if len(publicKey) != ed25519.PublicKeySize || !crypto.URNMatchesPublicKey(recipientURN, publicKey) {
		return fmt.Errorf("mailbox access denied: authenticated identity does not own recipient URN")
	}
	return nil
}

// AuthenticatedURN returns the default namespace URN for the transport identity.
func AuthenticatedURN(ctx context.Context) string {
	publicKey, _ := ctx.Value(authenticatedKeyContext{}).([]byte)
	if len(publicKey) != ed25519.PublicKeySize {
		return ""
	}
	return (&crypto.IdentityKeyPair{PublicKey: ed25519.PublicKey(publicKey)}).URN()
}
