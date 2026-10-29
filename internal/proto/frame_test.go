package proto

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

// frameHeader returns a frame header declaring a body of n bytes.
func frameHeader(t Type, n uint32) []byte {
	hdr := make([]byte, headerLen)
	hdr[0] = byte(t)
	binary.BigEndian.PutUint32(hdr[1:], n)
	return hdr
}

func TestFrameRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		typ  Type
		body []byte
	}{
		{"empty", TypePing, nil},
		{"text", TypeOut, []byte("hello\n")},
		{"binary", TypeOut, []byte{0, 1, 0xff}},
		{"exit", TypeExit, []byte(`{"code":1}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := WriteFrame(&buf, tc.typ, tc.body); err != nil {
				t.Fatalf("WriteFrame: %v", err)
			}
			if got, want := buf.Len(), headerLen+len(tc.body); got != want {
				t.Fatalf("frame size = %d, want %d", got, want)
			}
			typ, body, err := ReadFrame(&buf)
			if err != nil {
				t.Fatalf("ReadFrame: %v", err)
			}
			if typ != tc.typ || !bytes.Equal(body, tc.body) {
				t.Fatalf("got type 0x%02x body %q, want type 0x%02x body %q", byte(typ), body, byte(tc.typ), tc.body)
			}
			if _, _, err := ReadFrame(&buf); !errors.Is(err, io.EOF) {
				t.Fatalf("read after last frame: got %v, want io.EOF", err)
			}
		})
	}

	// A truncated frame is an unexpected EOF; only an empty stream is a clean EOF.
	var buf bytes.Buffer
	if err := WriteFrame(&buf, TypeOut, []byte("abcdef")); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	frame := buf.Bytes()
	for _, cut := range []int{1, headerLen, headerLen + 3} {
		if _, _, err := ReadFrame(bytes.NewReader(frame[:cut])); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("cut at %d: got %v, want io.ErrUnexpectedEOF", cut, err)
		}
	}
	if _, _, err := ReadFrame(bytes.NewReader(nil)); !errors.Is(err, io.EOF) {
		t.Errorf("empty stream: got %v, want io.EOF", err)
	}
}

func TestFrameOversize(t *testing.T) {
	// A declared length above the limit is rejected before any body is read.
	if _, _, err := ReadFrame(bytes.NewReader(frameHeader(TypeOut, MaxFrameLen+1))); !errors.Is(err, ErrFrameTooLarge) {
		t.Errorf("read oversize: got %v, want ErrFrameTooLarge", err)
	}

	// The limit itself is accepted; with no body behind it the read reports truncation.
	if _, _, err := ReadFrame(bytes.NewReader(frameHeader(TypeOut, MaxFrameLen))); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("read at limit: got %v, want io.ErrUnexpectedEOF", err)
	}

	var buf bytes.Buffer
	if err := WriteFrame(&buf, TypeOut, make([]byte, MaxFrameLen+1)); !errors.Is(err, ErrFrameTooLarge) {
		t.Errorf("write oversize: got %v, want ErrFrameTooLarge", err)
	}
	if buf.Len() != 0 {
		t.Errorf("oversize write left %d bytes on the stream", buf.Len())
	}
}
