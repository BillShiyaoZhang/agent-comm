package v2

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"
)

// ValidateBody checks that a gateway-opened body is the supported Agent Comm
// JSON message format. It cannot tell whether the text is truthful or whether
// an endpoint embedded another ciphertext inside that text.
func ValidateBody(raw []byte) error {
	if len(raw) == 0 || len(raw) > 262144+8192 {
		return errors.New("v2 body too large or empty")
	}
	keys := make(map[string]bool)
	check := json.NewDecoder(bytes.NewReader(raw))
	start, err := check.Token()
	if err != nil || start != json.Delim('{') {
		return errors.New("v2 body must be a JSON object")
	}
	for check.More() {
		keyToken, err := check.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok || keys[key] {
			return errors.New("duplicate or invalid v2 body field")
		}
		keys[key] = true
		var value json.RawMessage
		if err := check.Decode(&value); err != nil {
			return err
		}
	}
	if end, err := check.Token(); err != nil || end != json.Delim('}') {
		return errors.New("unterminated v2 body")
	}
	var trailing any
	if err := check.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON after v2 body")
	}
	var body struct {
		AgentComm      int    `json:"agent_comm"`
		Text           string `json:"text"`
		ConversationID string `json:"conversation_id"`
		InReplyTo      string `json:"in_reply_to"`
		TaskID         string `json:"task_id"`
		Kind           string `json:"kind"`
		Deadline       string `json:"deadline"`
		HopLimit       *int   `json:"hop_limit"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON after v2 body")
	}
	if body.AgentComm != 2 || len(body.Text) == 0 || len(body.Text) > 262144 || len(body.ConversationID) > 256 || len(body.InReplyTo) > 256 || len(body.TaskID) > 256 || len(body.Kind) > 256 {
		return errors.New("invalid v2 body fields")
	}
	if body.HopLimit != nil && (*body.HopLimit < 0 || *body.HopLimit > 64) {
		return errors.New("invalid v2 hop limit")
	}
	if body.Deadline != "" {
		if _, err := time.Parse(time.RFC3339, body.Deadline); err != nil {
			return errors.New("invalid v2 deadline")
		}
	}
	return nil
}
