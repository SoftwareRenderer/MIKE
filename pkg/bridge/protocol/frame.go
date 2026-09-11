// Package protocol defines the wire format shared by the host client and the in-container
// agent: a 9-byte header (message ID, type, length) plus a typed payload, with chunk frames
// streaming a body before its final result.
// Method IDs are permanent. 0x05-0x08 were the shell methods and are retired - terminals do
// not go through the bridge - so a stale agent gets "unknown method", not a wrong handler.
package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Message types.
const (
	TypeRequest      uint8 = 0x01
	TypeResult       uint8 = 0x02
	TypeChunk        uint8 = 0x03
	TypeError        uint8 = 0x04
	TypeNotification uint8 = 0x05
)

// MessageTypeName returns the human-readable name for a message type.
func MessageTypeName(t uint8) string {
	return messageTypeString(t)
}

func messageTypeString(t uint8) string {
	switch t {
	case TypeRequest:
		return "Request"
	case TypeResult:
		return "Result"
	case TypeChunk:
		return "Chunk"
	case TypeError:
		return "Error"
	case TypeNotification:
		return "Notification"
	default:
		return fmt.Sprintf("Unknown(%d)", t)
	}
}

// Frame is a single protocol message.
type Frame struct {
	MsgID   uint32
	Type    uint8
	Payload []byte
}

// HeaderSize is the fixed frame header: MsgID (4) + Type (1) + Length (4) = 9 bytes.
const HeaderSize = 9

// MaxFramePayload bounds one frame's payload: the length is a 4-byte field, so allocating from
// it before reading hands the host whatever number a peer claims.
const MaxFramePayload = 64 << 20

// MaxReadBytes bounds the body of one chunked response. handleRead refuses the file in the guest
// and the client stops collecting at the same number: a bound held only inside the container is
// no bound, because an older bridge keeps streaming.
const MaxReadBytes = 16 << 20

// MaxWriteBytes bounds one write_bytes payload, held to MaxReadBytes' rule: the guest refuses
// before it touches the filesystem, the client refuses before it sends. Base64 crosses the frame at
// 4/3 of the bytes, so this stays inside MaxFramePayload; it is server.MaxUploadSize.
const MaxWriteBytes = 20 << 20

var (
	ErrFrameTooLarge    = errors.New("frame payload exceeds the bridge limit")
	ErrResponseTooLarge = errors.New("response exceeds the bridge limit")
)

// ReadFrame reads a single frame from r.
// Returns io.ErrUnexpectedEOF if the connection is closed mid-frame.
func ReadFrame(r io.Reader) (*Frame, error) {
	header := make([]byte, HeaderSize)
	if _, err := io.ReadFull(r, header); err != nil {
		if err == io.EOF {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("read header: %w", err)
	}

	msgID := binary.BigEndian.Uint32(header[0:4])
	typ := header[4]
	length := binary.BigEndian.Uint32(header[5:9])

	if length > MaxFramePayload {
		return nil, fmt.Errorf("%w: %d bytes declared, limit %d", ErrFrameTooLarge, length, MaxFramePayload)
	}

	payload := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			if err == io.EOF {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, fmt.Errorf("read payload (%d bytes): %w", length, err)
		}
	}

	return &Frame{MsgID: msgID, Type: typ, Payload: payload}, nil
}

// WriteFrame writes a single frame to w.
func WriteFrame(w io.Writer, f *Frame) error {
	header := make([]byte, HeaderSize)
	binary.BigEndian.PutUint32(header[0:4], f.MsgID)
	header[4] = f.Type
	binary.BigEndian.PutUint32(header[5:9], uint32(len(f.Payload)))

	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	if len(f.Payload) > 0 {
		if _, err := w.Write(f.Payload); err != nil {
			return fmt.Errorf("write payload: %w", err)
		}
	}
	return nil
}

// NewResult creates a Result frame for the given MsgID.
func NewResult(msgID uint32, payload []byte) *Frame {
	return &Frame{MsgID: msgID, Type: TypeResult, Payload: payload}
}

// NewChunk creates a Chunk frame for the given MsgID.
func NewChunk(msgID uint32, payload []byte) *Frame {
	return &Frame{MsgID: msgID, Type: TypeChunk, Payload: payload}
}

// NewError creates an Error frame for the given MsgID with a string message.
func NewError(msgID uint32, msg string) *Frame {
	return &Frame{MsgID: msgID, Type: TypeError, Payload: []byte(msg)}
}

// ErrorMsg returns the error message from an Error frame's payload.
// Returns empty string if the frame is not an Error type.
func (f *Frame) ErrorMsg() string {
	if f.Type != TypeError {
		return ""
	}
	return string(f.Payload)
}

// String returns a debug string for the frame.
func (f *Frame) String() string {
	size := len(f.Payload)
	if size > 80 {
		size = 80
	}
	return fmt.Sprintf("Frame{MsgID:%d Type:%s Payload:%dB %q}",
		f.MsgID, messageTypeString(f.Type), len(f.Payload), string(f.Payload[:size]))
}
