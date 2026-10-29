// Package proto defines the sendaway wire protocol (protocol 1): length-prefixed
// frames and the JSON payloads carried in them.
package proto

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Protocol is the wire protocol version. Peers with different versions must not exchange requests.
const Protocol = 1

// MaxFrameLen is the largest frame body in bytes.
const MaxFrameLen = 16 << 20

// ErrFrameTooLarge reports a frame whose body length exceeds MaxFrameLen.
var ErrFrameTooLarge = errors.New("frame exceeds maximum length")

// Type is the one-byte frame type.
type Type byte

// Frame types. The remote side sends TypeHello through TypeReady; the shim sends TypeCancel through TypePing.
const (
	TypeHello  Type = 0x01
	TypeOut    Type = 0x02
	TypeErr    Type = 0x03
	TypeNote   Type = 0x04
	TypeExit   Type = 0x05
	TypeReady  Type = 0x06
	TypeCancel Type = 0x10
	TypePushed Type = 0x11
	TypePing   Type = 0x12
)

// headerLen is the size of the type byte plus the uint32 body length.
const headerLen = 5

// WriteFrame writes one frame with the given type and body.
// It returns an error wrapping ErrFrameTooLarge if the body is longer than MaxFrameLen.
func WriteFrame(w io.Writer, t Type, body []byte) error {
	if len(body) > MaxFrameLen {
		return fmt.Errorf("write frame type 0x%02x: %w (%d bytes)", byte(t), ErrFrameTooLarge, len(body))
	}
	var hdr [headerLen]byte
	hdr[0] = byte(t)
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(body))) //nolint:gosec // length checked against MaxFrameLen above
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}

// ReadFrame reads one frame and returns its type and body.
// It returns io.EOF only when the stream ends at a frame boundary, and io.ErrUnexpectedEOF
// when it ends inside a frame. A declared length above MaxFrameLen is rejected before the
// body is read, with an error wrapping ErrFrameTooLarge.
func ReadFrame(r io.Reader) (Type, []byte, error) {
	var hdr [headerLen]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > MaxFrameLen {
		return 0, nil, fmt.Errorf("read frame type 0x%02x: %w (%d bytes)", hdr[0], ErrFrameTooLarge, n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return 0, nil, err
	}
	return Type(hdr[0]), body, nil
}
