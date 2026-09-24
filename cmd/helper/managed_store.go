package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// The explicit managed route only accepts a response to a v1 control request
// already authenticated and saved in this helper's inbox. The local remote
// bridge checks the owner's pairing; the platform independently checks the
// recipient's active issuer-signed managed identity before v1 admission.
func (ds *DaemonServer) validateManagedControlResponse(reply StoreRequest) error {
	if reply.Kind != "control.response" || reply.InReplyTo == "" || reply.ConversationID != "control:"+reply.InReplyTo {
		return errors.New("control response correlation required")
	}
	digest := sha256.Sum256([]byte(reply.Text))
	if reply.MessageID != "control-response-"+hex.EncodeToString(digest[:24]) {
		return errors.New("control response message ID does not match its body")
	}
	request, err := ds.mailbox.managedControlRequest(reply.InReplyTo)
	if err != nil {
		return fmt.Errorf("original control request unavailable: %w", err)
	}
	if request.Mode != "" || request.Kind != "control.request" || request.SenderURN != reply.RecipientURN || request.MessageID != reply.InReplyTo || request.ConversationID != reply.ConversationID || request.Deadline != reply.Deadline {
		return errors.New("reply does not match a saved v1 control request")
	}
	original, err := controlFields(request.Text, "request")
	if err != nil {
		return err
	}
	response, err := controlFields(reply.Text, "response")
	if err != nil {
		return err
	}
	for _, field := range []string{"protocol", "request_id", "method", "agent_urn", "console_urn", "deadline"} {
		if response[field] != original[field] {
			return fmt.Errorf("control %s mismatch", field)
		}
	}
	if original["protocol"] != "agent-comm-control/v1" || original["request_id"] != reply.InReplyTo || original["console_urn"] != reply.RecipientURN || original["agent_urn"] != ds.agent.Keys.Ed25519.URN() || original["deadline"] != reply.Deadline {
		return errors.New("control identity or deadline mismatch")
	}
	return nil
}

func controlFields(text, expectedType string) (map[string]string, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, errors.New("control body must be JSON")
	}
	fields := []string{"protocol", "type", "request_id", "method", "agent_urn", "console_urn", "deadline"}
	if expectedType == "response" {
		if len(raw) != len(fields)+1 {
			return nil, errors.New("unexpected control response fields")
		}
		_, result := raw["result"]
		_, failure := raw["error"]
		if result == failure {
			return nil, errors.New("control response needs exactly one result or error")
		}
	} else if len(raw) != len(fields)+1 {
		return nil, errors.New("unexpected control request fields")
	} else if _, ok := raw["params"]; !ok {
		return nil, errors.New("control request parameters missing")
	}
	values := make(map[string]string, len(fields))
	for _, field := range fields {
		var value string
		if err := json.Unmarshal(raw[field], &value); err != nil || value == "" {
			return nil, fmt.Errorf("invalid control %s", field)
		}
		values[field] = value
	}
	if values["type"] != expectedType {
		return nil, errors.New("control type mismatch")
	}
	return values, nil
}
