package protocol

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	cases := []struct {
		name   string
		msgID  uint32
		typ    uint8
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
		})
	}
}

func TestFrameTruncatedHeader(t *testing.T) {
	buf := bytes.NewBuffer([]byte{0, 0, 0}) // 3 bytes, need 9
	_, err := ReadFrame(buf)
	if err == nil {
		t.Fatal("expected error for truncated header")
	}
}

func TestFrameTruncatedPayload(t *testing.T) {
	// Header says 100 bytes payload, only 10 provided
	frame := NewResult(1, make([]byte, 100))
	var buf bytes.Buffer
	_ = WriteFrame(&buf, frame)
	buf.Truncate(HeaderSize + 10) // cut off 90 bytes of payload

	_, err := ReadFrame(&buf)
	if err == nil {
		t.Fatal("expected error for truncated payload")
	}
}

func TestRequestRoundTrip(t *testing.T) {
	params, _ := json.Marshal(map[string]any{
		"path":    "src/main.go",
		"offset":  float64(10),
		"limit":   float64(50),
	})

	frame := NewRequest(123, MethodRead, params)
	var buf bytes.Buffer
	_ = WriteFrame(&buf, frame)

	req, err := ReadRequest(&buf)
	if err != nil {
		t.Fatalf("ReadRequest: %v", err)
	}
	if req.MsgID != 123 {
		t.Errorf("MsgID = %d, want 123", req.MsgID)
	}
	if req.Method != MethodRead {
		t.Errorf("Method = %d, want %d", req.Method, MethodRead)
	}

	var p ReadParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if p.Path != "src/main.go" {
		t.Errorf("Path = %q, want %q", p.Path, "src/main.go")
	}
	if p.Offset != 10 {
		t.Errorf("Offset = %d, want 10", p.Offset)
	}
}

func TestRequestInvalidType(t *testing.T) {
	frame := NewResult(1, []byte("data"))
	var buf bytes.Buffer
	_ = WriteFrame(&buf, frame)

	_, err := ReadRequest(&buf)
	if err == nil {
		t.Fatal("expected error for non-request frame")
	}
}

func TestRequestEmptyJSON(t *testing.T) {
	frame := NewRequest(1, MethodBash, nil)
	var buf bytes.Buffer
	_ = WriteFrame(&buf, frame)

	req, err := ReadRequest(&buf)
	if err != nil {
		t.Fatalf("ReadRequest: %v", err)
	}
	if len(req.Params) != 0 {
		t.Errorf("Params = %d bytes, want 0", len(req.Params))
	}
}

func TestBashResultRoundTrip(t *testing.T) {
	meta, _ := json.Marshal(map[string]any{"duration_ms": 150})
	payload := NewBashResult(0, meta)

	result, err := ParseBashResult(payload)
	if err != nil {
		t.Fatalf("ParseBashResult: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
	if !bytes.Equal(result.Meta, meta) {
		t.Errorf("Meta mismatch")
	}
}

func TestBashResultNoMeta(t *testing.T) {
	payload := NewBashResult(1, nil)
	result, err := ParseBashResult(payload)
	if err != nil {
		t.Fatalf("ParseBashResult: %v", err)
	}
	if result.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", result.ExitCode)
	}
	if len(result.Meta) != 0 {
		t.Errorf("Meta = %d bytes, want 0", len(result.Meta))
	}
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

func TestShellOpenResultRoundTrip(t *testing.T) {
	payload := NewShellOpenResult(42)
	result, err := ParseShellOpenResult(payload)
	if err != nil {
		t.Fatalf("ParseShellOpenResult: %v", err)
	}
	if result.SessionID != 42 {
		t.Errorf("SessionID = %d, want 42", result.SessionID)
	}
}

func TestShellChunkRoundTrip(t *testing.T) {
	payload := NewShellChunk(7, []byte("$ echo hi\necho hi\nhi\n$ "))
	chunk, err := ParseShellChunk(payload)
	if err != nil {
		t.Fatalf("ParseShellChunk: %v", err)
	}
	if chunk.SessionID != 7 {
		t.Errorf("SessionID = %d, want 7", chunk.SessionID)
	}
	if string(chunk.Data) != "$ echo hi\necho hi\nhi\n$ " {
		t.Errorf("Data mismatch")
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

func TestShellIORequestRoundTrip(t *testing.T) {
	frame := NewShellIORequest(1, 42, []byte("ls -la\n"))
	var buf bytes.Buffer
	_ = WriteFrame(&buf, frame)

	f, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	params, err := ParseShellIOParams(f)
	if err != nil {
		t.Fatalf("ParseShellIOParams: %v", err)
	}
	if params.SessionID != 42 {
		t.Errorf("SessionID = %d, want 42", params.SessionID)
	}
	if string(params.Input) != "ls -la\n" {
		t.Errorf("Input = %q, want %q", params.Input, "ls -la\n")
	}
}

func TestShellResizeRequestRoundTrip(t *testing.T) {
	frame := NewShellResizeRequest(1, 99, 24, 80)
	var buf bytes.Buffer
	_ = WriteFrame(&buf, frame)

	f, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	params, err := ParseShellResizeParams(f)
	if err != nil {
		t.Fatalf("ParseShellResizeParams: %v", err)
	}
	if params.SessionID != 99 {
		t.Errorf("SessionID = %d, want 99", params.SessionID)
	}
	if params.Rows != 24 || params.Cols != 80 {
		t.Errorf("Rows/Cols = %d/%d, want 24/80", params.Rows, params.Cols)
	}
}

func TestShellCloseRequestRoundTrip(t *testing.T) {
	frame := NewShellCloseRequest(1, 55)
	var buf bytes.Buffer
	_ = WriteFrame(&buf, frame)

	f, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	params, err := ParseShellCloseParams(f)
	if err != nil {
		t.Fatalf("ParseShellCloseParams: %v", err)
	}
	if params.SessionID != 55 {
		t.Errorf("SessionID = %d, want 55", params.SessionID)
	}
}

func TestJSONParams(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"path":    "test.go",
		"timeout": float64(30),
	})
	var p JSONParams
	_ = json.Unmarshal(raw, &p)

	s, ok := p.GetString("path")
	if !ok || s != "test.go" {
		t.Errorf("GetString = %q, %v, want %q, true", s, ok, "test.go")
	}

	n, ok := p.GetInt64("timeout")
	if !ok || n != 30 {
		t.Errorf("GetInt64 = %d, %v, want 30, true", n, ok)
	}

	_, ok = p.GetString("missing")
	if ok {
		t.Error("expected false for missing key")
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
		{MethodShellOpen, "shell_open"},
		{MethodShellIO, "shell_io"},
		{MethodShellResize, "shell_resize"},
		{MethodShellClose, "shell_close"},
		{0xFF, "unknown(255)"},
	}
	for _, tc := range cases {
		got := methodString(tc.method)
		if got != tc.want {
			t.Errorf("methodString(%d) = %q, want %q", tc.method, got, tc.want)
		}
	}
}

func TestFrameValidate(t *testing.T) {
	validTypes := []uint8{TypeRequest, TypeResult, TypeChunk, TypeError}
	for _, typ := range validTypes {
		f := &Frame{Type: typ}
		if err := f.Validate(); err != nil {
			t.Errorf("Validate(Type=%d) = %v, want nil", typ, err)
		}
	}

	f := &Frame{Type: 0xFF}
	if err := f.Validate(); err == nil {
		t.Error("Validate(0xFF) = nil, want error")
	}
}

func TestErrorMsg(t *testing.T) {
	f := NewError(1, "file not found")
	if got := f.ErrorMsg(); got != "file not found" {
		t.Errorf("ErrorMsg = %q, want %q", got, "file not found")
	}

	f2 := NewResult(1, []byte("data"))
	if got := f2.ErrorMsg(); got != "" {
		t.Errorf("ErrorMsg on Result = %q, want empty", got)
	}
}

func TestLargePayload(t *testing.T) {
	// 10MB payload
	data := make([]byte, 10*1024*1024)
	data[len(data)-1] = 0xFF // mark end byte

	frame := NewChunk(1, data)
	var buf bytes.Buffer
	if err := WriteFrame(&buf, frame); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	got, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if len(got.Payload) != len(data) {
		t.Errorf("Payload size = %d, want %d", len(got.Payload), len(data))
	}
	if got.Payload[len(got.Payload)-1] != 0xFF {
		t.Error("end byte corrupted")
	}
}
