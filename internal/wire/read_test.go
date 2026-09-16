package wire

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

type byteReader struct{ io.Reader }

func (r byteReader) Read(p []byte) (int, error) { return r.Reader.Read(p[:1]) }

func TestReadFrameBoundsBeforePayloadRead(t *testing.T) {
	for _, size := range []uint32{0, MaxMessageSize + 1, ^uint32(0)} {
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], size)
		if _, err := ReadFrame(bytes.NewReader(header[:])); err == nil {
			t.Fatalf("accepted untrusted frame size %d", size)
		}
	}
	for _, input := range [][]byte{{0, 0}, {0, 0, 0, 2, 'x'}} {
		if _, err := ReadFrame(bytes.NewReader(input)); err == nil {
			t.Fatal("accepted truncated frame")
		}
	}
	payload, err := ReadFrame(byteReader{bytes.NewReader([]byte{0, 0, 0, 2, 'o', 'k'})})
	if err != nil || string(payload) != "ok" {
		t.Fatalf("fragmented valid frame: %q, %v", payload, err)
	}
}

func TestReadMessageStopsAtLimit(t *testing.T) {
	r := bytes.NewReader(make([]byte, MaxMessageSize+100))
	if _, err := ReadMessage(r); err == nil {
		t.Fatal("accepted oversized message")
	}
	if r.Len() != 99 {
		t.Fatalf("read beyond the bounded probe: %d bytes remain", r.Len())
	}
	if _, err := ReadMessage(bytes.NewReader(make([]byte, MaxMessageSize))); err != nil {
		t.Fatalf("rejected maximum sized message: %v", err)
	}
}
