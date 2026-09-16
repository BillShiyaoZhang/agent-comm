// Package wire bounds reads before allocating memory for untrusted peers.
package wire

import (
	"encoding/binary"
	"fmt"
	"io"
)

const MaxMessageSize = 1 << 20

// ReadFrame reads a complete uint32 header, then a bounded payload.
func ReadFrame(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > MaxMessageSize {
		return nil, fmt.Errorf("invalid frame size: %d", size)
	}
	payload := make([]byte, size)
	_, err := io.ReadFull(r, payload)
	return payload, err
}

// ReadMessage bounds protocols whose message ends at EOF.
func ReadMessage(r io.Reader) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(r, MaxMessageSize+1))
	if err != nil {
		return nil, err
	}
	if len(payload) == 0 || len(payload) > MaxMessageSize {
		return nil, fmt.Errorf("invalid message size: %d", len(payload))
	}
	return payload, nil
}
