package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
)

// Method identifiers. 0x05-0x08 (shell_open / shell_io / shell_resize / shell_close) are retired
// and stay reserved rather than being reused: terminals are served by a host-side PTY (see
// backend/sandbox and backend/term), and a stale bridge binary still sending one of these must get
// a clean "unknown method" error instead of reaching an unrelated handler.
const (
	MethodRead       uint8 = 0x01
	MethodWrite      uint8 = 0x02
	MethodEdit       uint8 = 0x03
	MethodBash       uint8 = 0x04
	MethodBashCancel uint8 = 0x09
	MethodLSP        uint8 = 0x0A
)

func methodString(m uint8) string {
	switch m {
	case MethodRead:
		return "read"
	case MethodWrite:
		return "write"
	case MethodEdit:
		return "edit"
	case MethodBash:
		return "bash"
	case MethodBashCancel:
		return "bash_cancel"
	case MethodLSP:
		return "lsp"
	default:
		return fmt.Sprintf("unknown(%d)", m)
	}
}

// Request represents a parsed request frame.
type Request struct {
	MsgID  uint32
	Method uint8
	Params []byte // raw JSON payload
}

// ParseRequest parses a Request from a Request frame.
func ParseRequest(frame *Frame) (*Request, error) {
	if len(frame.Payload) < 6 {
		return nil, fmt.Errorf("request payload too short (%d bytes)", len(frame.Payload))
	}
	method := frame.Payload[0]
	// payload[1] is reserved (0x00)
	jsonLen := binary.BigEndian.Uint32(frame.Payload[2:6])
	if uint32(len(frame.Payload)) < 6+jsonLen {
		return nil, fmt.Errorf("request payload truncated: need %d JSON bytes, have %d", jsonLen, len(frame.Payload)-6)
	}
	params := frame.Payload[6 : 6+jsonLen]

	// Validate JSON only for non-binary methods
	if jsonLen > 0 && !isBinaryMethod(method) {
		var js json.RawMessage
		if err := json.Unmarshal(params, &js); err != nil {
			return nil, fmt.Errorf("invalid JSON params: %w", err)
		}
	}

	return &Request{
		MsgID:  frame.MsgID,
		Method: method,
		Params: params,
	}, nil
}

func isBinaryMethod(m uint8) bool {
	switch m {
	case MethodBashCancel:
		return true
	}
	return false
}

// NewRequest creates a Request frame from method and JSON params.
func NewRequest(msgID uint32, method uint8, paramsJSON []byte) *Frame {
	// [method:1][reserved:1][jsonLen:4][json:params]
	payload := make([]byte, 2+4+len(paramsJSON))
	payload[0] = method
	payload[1] = 0 // reserved
	binary.BigEndian.PutUint32(payload[2:6], uint32(len(paramsJSON)))
	copy(payload[6:], paramsJSON)

	return &Frame{MsgID: msgID, Type: TypeRequest, Payload: payload}
}

// MethodString returns the human-readable method name.
func (r *Request) MethodString() string {
	return methodString(r.Method)
}

// --- Binary (non-JSON) request params ---

// newBinaryRequest builds a request frame carrying a single uint32 param.
// Payload layout: [method:1][reserved:1][jsonLen:4=4][value:4]
func newBinaryRequest(msgID uint32, method uint8, value uint32) *Frame {
	payload := make([]byte, 2+4+4) // method + reserved + jsonLen + value
	payload[0] = method
	binary.BigEndian.PutUint32(payload[2:6], uint32(4))
	binary.BigEndian.PutUint32(payload[6:10], value)

	return &Frame{MsgID: msgID, Type: TypeRequest, Payload: payload}
}

// BashCancelParams holds the target message ID for a bash cancel request.
type BashCancelParams struct {
	TargetMsgID uint32 // MsgID of the bash request to cancel
}

// ParseBashCancelParams parses bash_cancel params from request payload.
// Format: [targetMsgID:4]
func ParseBashCancelParams(frame *Frame) (BashCancelParams, error) {
	payload := frame.Payload
	if len(payload) < 10 {
		return BashCancelParams{}, fmt.Errorf("bash_cancel payload too short")
	}
	method := payload[0]
	if method != MethodBashCancel {
		return BashCancelParams{}, fmt.Errorf("expected bash_cancel method, got %s", methodString(method))
	}
	data := payload[6:]
	if len(data) < 4 {
		return BashCancelParams{}, fmt.Errorf("bash_cancel data too short (%d bytes)", len(data))
	}
	return BashCancelParams{TargetMsgID: binary.BigEndian.Uint32(data[0:4])}, nil
}

func NewBashCancelRequest(msgID uint32, targetMsgID uint32) *Frame {
	return newBinaryRequest(msgID, MethodBashCancel, targetMsgID)
}
