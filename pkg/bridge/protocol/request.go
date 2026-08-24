package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
)

// Method identifiers.
const (
	MethodRead        uint8 = 0x01
	MethodWrite       uint8 = 0x02
	MethodEdit        uint8 = 0x03
	MethodBash        uint8 = 0x04
	MethodShellOpen   uint8 = 0x05
	MethodShellIO     uint8 = 0x06
	MethodShellResize uint8 = 0x07
	MethodShellClose  uint8 = 0x08
	MethodBashCancel  uint8 = 0x09
	MethodLSP         uint8 = 0x0A
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
	case MethodShellOpen:
		return "shell_open"
	case MethodShellIO:
		return "shell_io"
	case MethodShellResize:
		return "shell_resize"
	case MethodShellClose:
		return "shell_close"
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
	case MethodShellIO, MethodShellResize, MethodShellClose, MethodBashCancel:
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

// --- Shell-specific binary params (not JSON) ---

// ShellIOParams holds the binary parameters for shell_io requests.
type ShellIOParams struct {
	SessionID uint32
	Input     []byte
}

// ParseShellIOParams parses shell_io params from request payload.
// Format: [sessionID:4][inputLen:4][input bytes]
func ParseShellIOParams(frame *Frame) (ShellIOParams, error) {
	payload := frame.Payload
	if len(payload) < 14 {
		return ShellIOParams{}, fmt.Errorf("shell_io payload too short")
	}
	method := payload[0]
	if method != MethodShellIO {
		return ShellIOParams{}, fmt.Errorf("expected shell_io method, got %s", methodString(method))
	}
	// Skip reserved + jsonLen fields (they're 0 for binary params)
	data := payload[6:] // skip [method][reserved][jsonLen:4]
	if len(data) < 8 {
		return ShellIOParams{}, fmt.Errorf("shell_io data too short (%d bytes)", len(data))
	}
	sessionID := binary.BigEndian.Uint32(data[0:4])
	inputLen := binary.BigEndian.Uint32(data[4:8])
	if uint32(len(data)) < 8+inputLen {
		return ShellIOParams{}, fmt.Errorf("shell_io input truncated")
	}
	return ShellIOParams{
		SessionID: sessionID,
		Input:     data[8 : 8+inputLen],
	}, nil
}

func NewShellIORequest(msgID uint32, sessionID uint32, input []byte) *Frame {
	// [method:1][reserved:1][jsonLen:4][sessionID:4][inputLen:4][input bytes]
	payload := make([]byte, 2+4+4+4+len(input))
	payload[0] = MethodShellIO
	binary.BigEndian.PutUint32(payload[2:6], uint32(4+4+len(input)))
	binary.BigEndian.PutUint32(payload[6:10], sessionID)
	binary.BigEndian.PutUint32(payload[10:14], uint32(len(input)))
	copy(payload[14:], input)

	return &Frame{MsgID: msgID, Type: TypeRequest, Payload: payload}
}

// ShellResizeParams holds the binary parameters for shell_resize requests.
type ShellResizeParams struct {
	SessionID uint32
	Rows      uint16
	Cols      uint16
}

// ParseShellResizeParams parses shell_resize params from request payload.
// Format: [sessionID:4][rows:2][cols:2]
func ParseShellResizeParams(frame *Frame) (ShellResizeParams, error) {
	payload := frame.Payload
	if len(payload) < 14 {
		return ShellResizeParams{}, fmt.Errorf("shell_resize payload too short")
	}
	method := payload[0]
	if method != MethodShellResize {
		return ShellResizeParams{}, fmt.Errorf("expected shell_resize method, got %s", methodString(method))
	}
	data := payload[6:]
	if len(data) < 8 {
		return ShellResizeParams{}, fmt.Errorf("shell_resize data too short (%d bytes)", len(data))
	}
	return ShellResizeParams{
		SessionID: binary.BigEndian.Uint32(data[0:4]),
		Rows:      binary.BigEndian.Uint16(data[4:6]),
		Cols:      binary.BigEndian.Uint16(data[6:8]),
	}, nil
}

func NewShellResizeRequest(msgID uint32, sessionID uint32, rows, cols uint16) *Frame {
	payload := make([]byte, 2+4+4+2+2) // method + reserved + jsonLen + sessionID + rows + cols
	payload[0] = MethodShellResize
	binary.BigEndian.PutUint32(payload[2:6], uint32(4+2+2))
	binary.BigEndian.PutUint32(payload[6:10], sessionID)
	binary.BigEndian.PutUint16(payload[10:12], rows)
	binary.BigEndian.PutUint16(payload[12:14], cols)

	return &Frame{MsgID: msgID, Type: TypeRequest, Payload: payload}
}

// ShellCloseParams holds the binary parameters for shell_close requests.
type ShellCloseParams struct {
	SessionID uint32
}

// ParseShellCloseParams parses shell_close params from request payload.
func ParseShellCloseParams(frame *Frame) (ShellCloseParams, error) {
	payload := frame.Payload
	if len(payload) < 10 {
		return ShellCloseParams{}, fmt.Errorf("shell_close payload too short")
	}
	method := payload[0]
	if method != MethodShellClose {
		return ShellCloseParams{}, fmt.Errorf("expected shell_close method, got %s", methodString(method))
	}
	data := payload[6:]
	if len(data) < 4 {
		return ShellCloseParams{}, fmt.Errorf("shell_close data too short")
	}
	return ShellCloseParams{SessionID: binary.BigEndian.Uint32(data[0:4])}, nil
}

// newBinaryRequest builds a request frame carrying a single uint32 param.
// Payload layout: [method:1][reserved:1][jsonLen:4=4][value:4]
func newBinaryRequest(msgID uint32, method uint8, value uint32) *Frame {
	payload := make([]byte, 2+4+4) // method + reserved + jsonLen + value
	payload[0] = method
	binary.BigEndian.PutUint32(payload[2:6], uint32(4))
	binary.BigEndian.PutUint32(payload[6:10], value)

	return &Frame{MsgID: msgID, Type: TypeRequest, Payload: payload}
}

func NewShellCloseRequest(msgID uint32, sessionID uint32) *Frame {
	return newBinaryRequest(msgID, MethodShellClose, sessionID)
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
