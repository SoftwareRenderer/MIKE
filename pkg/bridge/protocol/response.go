package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
)

// BashResult holds the parsed result of a bash command.
type BashResult struct {
	ExitCode int32
	Meta     []byte // raw JSON metadata (optional)
}

// ParseBashResult parses a bash result payload.
// Format: [exitCode:4 BE int32][metaLen:4 BE][meta JSON bytes]
func ParseBashResult(payload []byte) (BashResult, error) {
	if len(payload) < 4 {
		return BashResult{}, fmt.Errorf("bash result too short (%d bytes)", len(payload))
	}
	exitCode := int32(binary.BigEndian.Uint32(payload[0:4]))
	if len(payload) == 4 {
		return BashResult{ExitCode: exitCode}, nil
	}
	if len(payload) < 8 {
		return BashResult{}, fmt.Errorf("bash result meta header too short")
	}
	metaLen := binary.BigEndian.Uint32(payload[4:8])
	if uint32(len(payload)) < 8+metaLen {
		return BashResult{}, fmt.Errorf("bash result meta truncated")
	}
	return BashResult{
		ExitCode: exitCode,
		Meta:     payload[8 : 8+metaLen],
	}, nil
}

// NewBashResult creates a bash result payload.
func NewBashResult(exitCode int32, metaJSON []byte) []byte {
	total := 4 + 4 + len(metaJSON) // exitCode + metaLen + meta
	payload := make([]byte, total)
	binary.BigEndian.PutUint32(payload[0:4], uint32(exitCode))
	binary.BigEndian.PutUint32(payload[4:8], uint32(len(metaJSON)))
	copy(payload[8:], metaJSON)
	return payload
}

// BashChunk holds the parsed data of a bash output chunk.
type BashChunk struct {
	Stream uint8 // 0 = stdout, 1 = stderr
	Data   []byte
}

// ParseBashChunk parses a bash chunk payload.
// Format: [stream:1][data bytes]
func ParseBashChunk(payload []byte) (BashChunk, error) {
	if len(payload) < 1 {
		return BashChunk{}, fmt.Errorf("bash chunk too short")
	}
	return BashChunk{
		Stream: payload[0],
		Data:   payload[1:],
	}, nil
}

// NewBashChunk creates a bash chunk payload.
func NewBashChunk(stream uint8, data []byte) []byte {
	payload := make([]byte, 1+len(data))
	payload[0] = stream
	copy(payload[1:], data)
	return payload
}

// Uint64Result parses a simple uint64 result (used by write/edit).
func Uint64Result(payload []byte) (uint64, error) {
	if len(payload) != 8 {
		return 0, fmt.Errorf("uint64 result: expected 8 bytes, got %d", len(payload))
	}
	return binary.BigEndian.Uint64(payload), nil
}

// NewUint64Result creates a uint64 result payload.
func NewUint64Result(n uint64) []byte {
	payload := make([]byte, 8)
	binary.BigEndian.PutUint64(payload, n)
	return payload
}

// Edit represents a single text replacement.
type Edit struct {
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

// EditParams holds the parsed parameters for an edit request.
type EditParams struct {
	Path  string `json:"path"`
	Edits []Edit `json:"edits"`
}

// ReadParams holds the parsed parameters for a read request.
type ReadParams struct {
	Path string `json:"path"`
}

// WriteParams holds the parsed parameters for a write request.
type WriteParams struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// BashParams holds the parsed parameters for a bash request.
type BashParams struct {
	Command string `json:"command"`
	Timeout int64  `json:"timeout"`
}

// LSPParams holds the parsed parameters for an LSP request.
type LSPParams struct {
	Raw     json.RawMessage `json:"raw"`               // raw JSON-RPC message
	Command string          `json:"command,omitempty"` // LSP server command (first request only)
	Args    []string        `json:"args,omitempty"`    // LSP server args (first request only)
}
