package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json/v2"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		msgID   uint32
		typ     uint8
		payload []byte
	}{
		{"empty", 1, TypeResult, nil},
		{"small", 42, TypeChunk, []byte("hello")},
		{"zero-msgid", 0, TypeRequest, []byte{0x01, 0x00, 0, 0, 0, 5}},
		{"large", 999, TypeError, make([]byte, 100_000)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frame := &Frame{MsgID: tc.msgID, Type: tc.typ, Payload: tc.payload}
			var buf bytes.Buffer
			if err := WriteFrame(&buf, frame); err != nil {
				t.Fatalf("WriteFrame: %v", err)
			}

			got, err := ReadFrame(&buf)
			if err != nil {
				t.Fatalf("ReadFrame: %v", err)
			}
			if got.MsgID != frame.MsgID {
				t.Errorf("MsgID = %d, want %d", got.MsgID, frame.MsgID)
			}
			if got.Type != frame.Type {
				t.Errorf("Type = %d, want %d", got.Type, frame.Type)
			}
			if !bytes.Equal(got.Payload, frame.Payload) {
				t.Errorf("Payload mismatch: got %d bytes, want %d", len(got.Payload), len(frame.Payload))
			}
			// ErrorMsg reads the payload of an error frame and nothing else: a Result's bytes are a
			// size or a body, never a message to show someone.
			if got.Type == TypeError {
				if got.ErrorMsg() != string(frame.Payload) {
					t.Errorf("ErrorMsg = %q, want the frame's error payload", got.ErrorMsg())
				}
			} else if got.ErrorMsg() != "" {
				t.Errorf("ErrorMsg on a %s frame = %q, want nothing", MessageTypeName(got.Type), got.ErrorMsg())
			}
		})
	}
}

// A frame that stops short of its declared length is never a short frame: a truncated header, or a
// payload that quits early, is an error — so no caller assembles half a message.
func TestReadFrameRefusesATruncatedFrame(t *testing.T) {
	cases := []struct {
		name     string
		frame    *Frame
		truncate int
	}{
		{"header", NewResult(1, []byte("payload")), 3},
		{"payload", NewResult(1, make([]byte, 100)), HeaderSize + 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := WriteFrame(&buf, tc.frame); err != nil {
				t.Fatalf("WriteFrame: %v", err)
			}
			buf.Truncate(tc.truncate)
			if _, err := ReadFrame(&buf); err == nil {
				t.Fatal("a truncated frame was read as a frame")
			}
		})
	}
}

// A request's params are the tool's own JSON, and their absence is a request too: params that were
// never sent come back as nothing, not as an error and not as "null".
func TestRequestRoundTrip(t *testing.T) {
	cases := []struct {
		name   string
		params []byte
		path   string
	}{
		{"with params", mustMarshal(t, map[string]any{"path": "src/main.go"}), "src/main.go"},
		{"without params", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := ReadFrame(frameBytes(t, NewRequest(123, MethodRead, tc.params)))
			if err != nil {
				t.Fatalf("ReadFrame: %v", err)
			}
			req, err := ParseRequest(f)
			if err != nil {
				t.Fatalf("ParseRequest: %v", err)
			}
			if req.MsgID != 123 {
				t.Errorf("MsgID = %d, want 123", req.MsgID)
			}
			if req.Method != MethodRead {
				t.Errorf("Method = %d, want %d", req.Method, MethodRead)
			}
			if !bytes.Equal(req.Params, tc.params) {
				t.Errorf("Params = %x, want %x", req.Params, tc.params)
			}
			if tc.params == nil {
				return
			}

			var p ReadParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				t.Fatalf("unmarshal params: %v", err)
			}
			if p.Path != tc.path {
				t.Errorf("Path = %q, want %q", p.Path, tc.path)
			}
		})
	}
}

// The bash result's meta is optional: what was not sent comes back as nothing rather than as "null"
// or as a parse error, so the caller that reports a duration never has to guess.
func TestBashResultRoundTrip(t *testing.T) {
	meta, _ := json.Marshal(map[string]any{"duration_ms": 150})
	cases := []struct {
		name     string
		exitCode int32
		meta     []byte
	}{
		{"with meta", 0, meta},
		{"without meta", 1, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := ParseBashResult(NewBashResult(tc.exitCode, tc.meta))
			if err != nil {
				t.Fatalf("ParseBashResult: %v", err)
			}
			if result.ExitCode != tc.exitCode {
				t.Errorf("ExitCode = %d, want %d", result.ExitCode, tc.exitCode)
			}
			if !bytes.Equal(result.Meta, tc.meta) {
				t.Errorf("Meta = %x, want %x", result.Meta, tc.meta)
			}
		})
	}
}

// frameBytes encodes a frame the way it goes on the wire.
func frameBytes(t *testing.T, f *Frame) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	if err := WriteFrame(&buf, f); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	return &buf
}

// mustMarshal is the params a test declares, or nothing.
func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func TestBashChunkRoundTrip(t *testing.T) {
	payload := NewBashChunk(0, []byte("hello stdout\n"))
	chunk, err := ParseBashChunk(payload)
	if err != nil {
		t.Fatalf("ParseBashChunk: %v", err)
	}
	if chunk.Stream != 0 {
		t.Errorf("Stream = %d, want 0", chunk.Stream)
	}
	if string(chunk.Data) != "hello stdout\n" {
		t.Errorf("Data = %q, want %q", chunk.Data, "hello stdout\n")
	}
}

func TestUint64ResultRoundTrip(t *testing.T) {
	payload := NewUint64Result(12345)
	n, err := Uint64Result(payload)
	if err != nil {
		t.Fatalf("Uint64Result: %v", err)
	}
	if n != 12345 {
		t.Errorf("n = %d, want 12345", n)
	}
}

// TestBinaryMethodParamFraming pins the two framing rules for binary methods
// (currently just bash_cancel):
//
//  1. Params are read by offset from payload[6:], so the jsonLen field plays no
//     part in decoding — a mismatched jsonLen must not shift the decoded value.
//  2. ParseRequest skips JSON validation for them (see isBinaryMethod), so params
//     that are not JSON at all still parse. jsonLen *is* still bounds-checked,
//     which is why the builders always write a truthful length.
func TestBinaryMethodParamFraming(t *testing.T) {
	// Layout: [method][reserved][jsonLen: 4 bytes][targetMsgID: 4 bytes]
	payload := make([]byte, 6+4)
	payload[0] = MethodBashCancel
	binary.BigEndian.PutUint32(payload[2:6], 0xDEADBEEF) // deliberately wrong jsonLen
	binary.BigEndian.PutUint32(payload[6:10], 789)       // target MsgID

	params, err := ParseBashCancelParams(&Frame{Payload: payload})
	if err != nil {
		t.Fatalf("ParseBashCancelParams: %v", err)
	}
	if params.TargetMsgID != 789 {
		t.Errorf("TargetMsgID = %d, want 789", params.TargetMsgID)
	}

	// A well-formed frame whose params are raw bytes, not JSON.
	req, err := ParseRequest(NewBashCancelRequest(1, 789))
	if err != nil {
		t.Fatalf("ParseRequest on a binary-method frame = %v, want nil", err)
	}
	if req.Method != MethodBashCancel {
		t.Errorf("Method = %s, want bash_cancel", req.MethodString())
	}
}

func TestMethodString(t *testing.T) {
	cases := []struct {
		method uint8
		want   string
	}{
		{MethodRead, "read"},
		{MethodWrite, "write"},
		{MethodEdit, "edit"},
		{MethodBash, "bash"},
		{MethodBashCancel, "bash_cancel"},
		{MethodLSP, "lsp"},
		// 0x05-0x08 were the retired shell methods and 0x0B was write_bytes; all stay unassigned.
		{0x05, "unknown(5)"},
		{0x08, "unknown(8)"},
		{0x0B, "unknown(11)"},
		{0xFF, "unknown(255)"},
	}
	for _, tc := range cases {
		got := methodString(tc.method)
		if got != tc.want {
			t.Errorf("methodString(%d) = %q, want %q", tc.method, got, tc.want)
		}
	}
}
