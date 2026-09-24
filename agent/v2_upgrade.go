package agent

import (
	"errors"
	"os"

	"github.com/BillShiyaoZhang/agent-comm/v2"
)

// LegacySendError is returned by SendMessage when this identity has opted in
// to v2. Callers can inspect Code and route through the helper's v2 API.
type LegacySendError struct {
	Code string
}

func (e *LegacySendError) Error() string {
	if e.Code == "policy_root_required" {
		return "policy_root_required: independently pin the v2 policy root, then use POST /api/v2/mq/store through the local helper"
	}
	if e.Code == "policy_unavailable" {
		return "policy_unavailable: v2 policy root is invalid; inspect GET /api/v2/disclosure"
	}
	return "upgrade_required: use the local helper POST /api/v2/mq/store; inspect GET /api/v2/disclosure for mode and consent"
}

func (a *Agent) legacyDirectSendGuard() error {
	if a == nil || a.Keys == nil {
		return errors.New("agent identity unavailable")
	}
	_, err := v2.LoadPolicyRoot(a.Keys.KeysDir)
	if err == nil {
		return &LegacySendError{Code: "upgrade_required"}
	}
	if errors.Is(err, os.ErrNotExist) {
		if a.MQHTTPClient != nil {
			return &LegacySendError{Code: "policy_root_required"}
		}
		return nil
	}
	return &LegacySendError{Code: "policy_unavailable"}
}
