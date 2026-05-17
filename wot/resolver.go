// Package wot provides Web of Trust: signed trust claims and transitive trust path resolution.
package wot

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/nousresearch/hermes-agent/agent-comm/proto"
	goproto "google.golang.org/protobuf/proto"
)

// ProtoID is the libp2p protocol identifier for WoT queries.
const WoTProtoID = "/hermes/agent-comm/wot/1.0.0"

// Resolver finds transitive trust paths between URNs.
// It uses a local Store for cached claims and queries remote peers for missing claims.
type Resolver struct {
	host  host.Host
	store *Store
	urn   string // our own URN
}

// NewResolver creates a new WoT resolver.
func NewResolver(h host.Host, store *Store) *Resolver {
	return &Resolver{host: h, store: store, urn: h.ID().String()}
}

// TrustPath represents a verified chain of trust from source to target.
type TrustPath struct {
	Claims    []*TrustClaim // ordered from source to target
	TrustedPK []byte        // target's trusted X25519 pubkey
	Depth     int           // number of hops
}

// IsTrusted returns true if target URN is trusted (via some trust path).
func (r *Resolver) IsTrusted(targetURN string) bool {
	_, err := r.FindTrustPath(context.Background(), targetURN)
	return err == nil
}

// FindTrustPath searches for a verified trust path from our URN to target.
// Returns the path and the target's trusted X25519 pubkey.
// Returns an error if no path exists or verification fails.
func (r *Resolver) FindTrustPath(ctx context.Context, targetURN string) (*TrustPath, error) {
	ourURN := r.store.keys.Ed25519.URN()

	// Self-trust: if target is us, trivially trusted
	if targetURN == ourURN {
		pk, err := r.store.GetPeerX25519PK(targetURN)
		if err != nil {
			return nil, fmt.Errorf("self key unknown")
		}
		return &TrustPath{TrustedPK: pk, Depth: 0}, nil
	}

	// BFS from our URN
	type bfsNode struct {
		urn    string
		path   []*TrustClaim
		visited map[string]bool
	}

	queue := []bfsNode{{urn: ourURN, path: nil, visited: map[string]bool{ourURN: true}}}
	visited := map[string]bool{ourURN: true}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		// Get all claims issued BY current.urn about anyone
		claimsByCurrent, err := r.store.GetClaimsBy(current.urn)
		if err != nil {
			continue
		}

		for _, claim := range claimsByCurrent {
			if claim.Level != proto.TrustLevel_TRUSTED {
				continue
			}
			if claim.SubjectUrn == targetURN {
				// Found path!
				fullPath := append(current.path, claim)
				// Verify the final claim's pubkey
				trustedPK := claim.SubjectX25519Pk
				if len(trustedPK) != 32 {
					continue
				}
				return &TrustPath{
					Claims:    fullPath,
					TrustedPK: trustedPK,
					Depth:     len(fullPath),
				}, nil
			}

			// Expand search: queue this subject if not visited
			// But first we need to fetch claims BY this subject
			if !visited[claim.SubjectUrn] {
				visited[claim.SubjectUrn] = true
				// Fetch claims about subject_urn from network, then add to store
				claims, err := r.FetchClaimsAbout(ctx, claim.SubjectUrn)
				if err == nil {
					for _, c := range claims {
						_ = r.store.AddClaim(c) // ignore errors (unverifiable, duplicate, etc.)
					}
				}
				// Now enqueue for BFS expansion (we'll pick up their claims in next iteration)
				queue = append(queue, bfsNode{
					urn: claim.SubjectUrn,
					path: append(current.path, claim),
				})
			}
		}
	}

	return nil, fmt.Errorf("no trust path found to %s", targetURN)
}

// FindTrustPathSimple is a synchronous version that only uses local store.
func (r *Resolver) FindTrustPathSimple(ctx context.Context, targetURN string) (*TrustPath, error) {
	ourURN := r.store.keys.Ed25519.URN()
	if targetURN == ourURN {
		pk, _ := r.store.GetPeerX25519PK(targetURN)
		return &TrustPath{TrustedPK: pk, Depth: 0}, nil
	}

	visited := map[string]bool{ourURN: true}
	type queueItem struct {
		urn  string
		path []*TrustClaim
	}
	queue := []queueItem{{urn: ourURN, path: nil}}

	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]

		claimsByCurrent, err := r.store.GetClaimsBy(item.urn)
		if err != nil {
			continue
		}

		for _, claim := range claimsByCurrent {
			if claim.Level != proto.TrustLevel_TRUSTED {
				continue
			}
			if claim.SubjectUrn == targetURN {
				fullPath := append(item.path, claim)
				return &TrustPath{
					Claims:    fullPath,
					TrustedPK: claim.SubjectX25519Pk,
					Depth:     len(fullPath),
				}, nil
			}
			if !visited[claim.SubjectUrn] {
				visited[claim.SubjectUrn] = true
				queue = append(queue, queueItem{
					urn:  claim.SubjectUrn,
					path: append(item.path, claim),
				})
			}
		}
	}
	return nil, fmt.Errorf("no trust path found")
}

// FetchClaimsAbout queries a remote peer for all claims they know about a subject URN.
// Tries all connected peers, returns results from the first that responds.
func (r *Resolver) FetchClaimsAbout(ctx context.Context, subjectURN string) ([]*TrustClaim, error) {
	query := &proto.WOTRequest{Op: &proto.WOTRequest_Query{Query: &proto.WOTQuery{SubjectUrn: subjectURN}}}
	queryBytes, err := goproto.Marshal(query)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for _, p := range r.host.Network().Peers() {
		ctx2, cancel := context.WithTimeout(ctx, 5*time.Second)
		stream, err := r.host.NewStream(ctx2, p, protocol.ID(WoTProtoID))
		if err != nil {
			cancel()
			lastErr = err
			continue
		}

		// Send query
		if err := writeUint32BE(stream, uint32(len(queryBytes))); err != nil {
			stream.Close()
			cancel()
			lastErr = err
			continue
		}
		if _, err := stream.Write(queryBytes); err != nil {
			stream.Close()
			cancel()
			lastErr = err
			continue
		}
		stream.CloseWrite()

		// Read response
		sizeBuf := make([]byte, 4)
		if _, err := stream.Read(sizeBuf); err != nil {
			stream.Close()
			cancel()
			lastErr = err
			continue
		}
		size := binary.BigEndian.Uint32(sizeBuf)
		respBytes := make([]byte, size)
		if _, err := io.ReadFull(stream, respBytes); err != nil {
			stream.Close()
			cancel()
			lastErr = err
			continue
		}
		stream.Close()
		cancel()

		var wrapper proto.WOTResponseWrapper
		if err := goproto.Unmarshal(respBytes, &wrapper); err != nil {
			lastErr = err
			continue
		}

		out := make([]*TrustClaim, len(wrapper.Response.Claims))
		for i, c := range wrapper.Response.Claims {
			out[i] = &TrustClaim{TrustClaim: c}
		}
		return out, nil
	}

	if lastErr != nil {
		return nil, fmt.Errorf("all peers failed: last error: %w", lastErr)
	}
	return nil, fmt.Errorf("no connected peers available")
}

// HandleWOTStream handles incoming WoT query streams.
func HandleWOTStream(stream network.Stream, store *Store) {
	defer stream.Close()

	sizeBuf := make([]byte, 4)
	if _, err := stream.Read(sizeBuf); err != nil {
		return
	}
	size := binary.BigEndian.Uint32(sizeBuf)
	reqBytes := make([]byte, size)
	if _, err := io.ReadFull(stream, reqBytes); err != nil {
		return
	}

	var req proto.WOTRequest
	if err := goproto.Unmarshal(reqBytes, &req); err != nil {
		return
	}

	var resp proto.WOTResponse
	if q := req.GetQuery(); q != nil {
		claims, err := store.GetClaimsAbout(q.SubjectUrn)
		if err == nil {
			for _, c := range claims {
				resp.Claims = append(resp.Claims, c.TrustClaim)
			}
		}
	}

	respBytes, _ := goproto.Marshal(&proto.WOTResponseWrapper{Response: &resp})
	respLen := make([]byte, 4)
	binary.BigEndian.PutUint32(respLen, uint32(len(respBytes)))
	stream.Write(respLen)
	stream.Write(respBytes)
}

// RegisterWOTHandler registers the WoT stream handler on the host.
func RegisterWOTHandler(h host.Host, store *Store) {
	h.SetStreamHandler(protocol.ID(WoTProtoID), func(stream network.Stream) {
		HandleWOTStream(stream, store)
	})
}

// writeUint32BE writes a uint32 in big-endian format.
func writeUint32BE(w io.Writer, v uint32) error {
	buf := [4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
	_, err := w.Write(buf[:])
	return err
}

// ---- Verification helpers ----

// VerifyTrustPath verifies the signature on each claim in the path.
// Returns an error if any signature is invalid.
func VerifyTrustPath(path []*TrustClaim, getEd25519PK func(urn string) ([]byte, error)) error {
	for i, claim := range path {
		issuerPK, err := getEd25519PK(claim.IssuerUrn)
		if err != nil {
			return fmt.Errorf("claim[%d]: issuer %s unknown: %w", i, claim.IssuerUrn, err)
		}
		if err := claim.Verify(issuerPK); err != nil {
			return fmt.Errorf("claim[%d]: signature invalid: %w", i, err)
		}
	}
	return nil
}

// BuildAdjacencyMap builds an adjacency map from a list of claims.
// Key: issuer URN → Value: list of subject URNs with TRUSTED level.
func BuildAdjacencyMap(claims []*TrustClaim) map[string][]string {
	adj := make(map[string][]string)
	for _, c := range claims {
		if c.Level == proto.TrustLevel_TRUSTED {
			adj[c.IssuerUrn] = append(adj[c.IssuerUrn], c.SubjectUrn)
		}
	}
	return adj
}

// BFSPaths finds all simple paths from source to target in the trust graph.
// Returns paths of at most maxDepth hops.
func BFSPaths(adj map[string][]string, source, target string, maxDepth int) [][]string {
	var results [][]string
	var bfs func(node string, depth int, path []string)
	visited := make(map[string]bool)

	bfs = func(node string, depth int, path []string) {
		if depth > maxDepth {
			return
		}
		if node == target {
			cp := make([]string, len(path))
			copy(cp, path)
			results = append(results, cp)
			return
		}
		visited[node] = true
		path = append(path, node)
		for _, next := range adj[node] {
			if !visited[next] {
				bfs(next, depth+1, path)
			}
		}
		path = path[:len(path)-1]
		visited[node] = false
	}

	bfs(source, 0, nil)
	return results
}