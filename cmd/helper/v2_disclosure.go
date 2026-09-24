package main

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/BillShiyaoZhang/agent-comm/v2"
)

// DisclosureStatus separates a verified platform claim from the owner's local
// disclosure choice. Unknown is represented by null, never by false.
type DisclosureStatus struct {
	State                     string  `json:"state"`
	PolicyVerified            bool    `json:"policy_verified"`
	PolicyRootPublicKey       *string `json:"policy_root_public_key"`
	PlatformID                *string `json:"platform_id"`
	Mode                      *string `json:"mode"`
	PolicyEpoch               *uint64 `json:"policy_epoch"`
	PolicyHash                *string `json:"policy_hash"`
	NotBefore                 *int64  `json:"not_before"`
	ExpiresAt                 *int64  `json:"expires_at"`
	GatewayKeyID              *string `json:"gateway_key_id"`
	LastVerifiedEpoch         uint64  `json:"last_verified_epoch"`
	PlatformCanDecrypt        *bool   `json:"platform_can_decrypt"`
	LocalComplianceAuthorized bool    `json:"local_compliance_authorized"`
	V2SendReady               bool    `json:"v2_send_ready"`
	LegacySendCode            string  `json:"legacy_send_code"`
	QuarantinedV1             int64   `json:"quarantined_v1"`
	QuarantinedV2             int64   `json:"quarantined_v2"`
}

func (ds *DaemonServer) disclosureStatus() (DisclosureStatus, error) {
	state := DisclosureStatus{State: "legacy_unconfigured", LegacySendCode: "policy_root_required"}
	var err error
	state.LastVerifiedEpoch, err = ds.mailbox.highestV2Epoch()
	if err != nil {
		return state, err
	}
	if err := ds.mailbox.db.QueryRow(`SELECT COUNT(*) FROM helper_outbox WHERE status='quarantined'`).Scan(&state.QuarantinedV1); err != nil {
		return state, err
	}
	if err := ds.mailbox.db.QueryRow(`SELECT COUNT(*) FROM helper_v2_outbox WHERE status='quarantined'`).Scan(&state.QuarantinedV2); err != nil {
		return state, err
	}
	if ds.v2 == nil {
		return state, nil
	}
	rootHex := hex.EncodeToString(ds.v2.root)
	state.PolicyRootPublicKey = &rootHex
	state.State = "policy_unavailable"
	state.LegacySendCode = "policy_unavailable"
	policy := ds.v2.currentPolicy()
	if policy == nil || policy.PlatformID != ds.v2.client.ExpectedPlatformID || v2.VerifyPolicy(policy, ds.v2.root, time.Now()) != nil || policy.Epoch < state.LastVerifiedEpoch {
		return state, nil
	}
	state.PolicyVerified = true
	state.PlatformID = &policy.PlatformID
	state.Mode = &policy.Mode
	state.PolicyEpoch = &policy.Epoch
	state.NotBefore = &policy.NotBefore
	state.ExpiresAt = &policy.ExpiresAt
	hash := v2.PolicyHash(policy)
	state.PolicyHash = &hash
	canDecrypt := policy.Mode == v2.ModeCompliance
	state.PlatformCanDecrypt = &canDecrypt
	if canDecrypt {
		state.GatewayKeyID = &policy.GatewayKeyID
		state.LocalComplianceAuthorized = v2.ComplianceAllowed(ds.agent.Keys.KeysDir, policy)
	}
	if canDecrypt && !state.LocalComplianceAuthorized {
		state.State = "consent_required"
		state.LegacySendCode = "consent_required"
		return state, nil
	}
	state.State = "ready"
	state.V2SendReady = true
	state.LegacySendCode = "upgrade_required"
	return state, nil
}

func (ds *DaemonServer) handleDisclosure(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	state, err := ds.disclosureStatus()
	if err != nil {
		http.Error(w, "Local disclosure state unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(state)
}

func writePolicyError(w http.ResponseWriter, status int, code string, state DisclosureStatus) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":         false,
		"code":            code,
		"disclosure":      state,
		"v2_store_path":   "/api/v2/mq/store",
		"disclosure_path": "/api/v2/disclosure",
	})
}

func (ds *DaemonServer) legacyStorePolicyError(w http.ResponseWriter) bool {
	state, err := ds.disclosureStatus()
	if err != nil {
		writePolicyError(w, http.StatusServiceUnavailable, "policy_unavailable", state)
		return true
	}
	code := state.LegacySendCode
	if code == "policy_root_required" {
		writePolicyError(w, http.StatusPreconditionRequired, code, state)
	} else if code == "consent_required" {
		writePolicyError(w, http.StatusForbidden, code, state)
	} else if code == "policy_unavailable" {
		writePolicyError(w, http.StatusServiceUnavailable, code, state)
	} else {
		writePolicyError(w, http.StatusConflict, "upgrade_required", state)
	}
	return true
}

func (m *mailbox) managedControlRequest(id string) (InboxMessage, error) {
	var payload []byte
	if err := m.db.QueryRow(`SELECT payload FROM helper_inbox WHERE message_id=?`, id).Scan(&payload); err != nil {
		return InboxMessage{}, err
	}
	var msg InboxMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return InboxMessage{}, err
	}
	return msg, nil
}
