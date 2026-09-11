package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
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
		"path": "src/main.go",
	})

	frame := NewRequest(123, MethodRead, params)
	var buf bytes.Buffer
	_ = WriteFrame(&buf, frame)

	f, err := ReadFrame(&buf)
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

	var p ReadParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if p.Path != "src/main.go" {
		t.Errorf("Path = %q, want %q", p.Path, "src/main.go")
	}
}

func TestRequestEmptyJSON(t *testing.T) {
	frame := NewRequest(1, MethodBash, nil)
	var buf bytes.Buffer
	_ = WriteFrame(&buf, frame)

	f, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	req, err := ParseRequest(f)
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
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
		{MethodWriteBytes, "write_bytes"},
		// 0x05-0x08 were the retired shell methods; they must stay unassigned.
		{0x05, "unknown(5)"},
		{0x08, "unknown(8)"},
		{0xFF, "unknown(255)"},
	}
	for _, tc := range cases {
		got := methodString(tc.method)
		if got != tc.want {
			t.Errorf("methodString(%d) = %q, want %q", tc.method, got, tc.want)
		}
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

// The control case is the shape the text write keeps: the same bytes through WriteParams are
// where the corruption happens.
func TestWriteBytesParamsHoldBytesThatAreNotText(t *testing.T) {
	raw := []byte{0x89, 'P', 'N', 'G', 0xff, 0xfe, 0x00, 0x80, 0xc3, 0x28, 0xa0}

	payload, err := json.Marshal(WriteBytesParams{Path: "/tmp/shot.png", Data: raw})
	if err != nil {
		t.Fatal(err)
	}
	var bytesBack WriteBytesParams
	if err := json.Unmarshal(payload, &bytesBack); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytesBack.Data, raw) {
		t.Errorf("params changed the bytes:\n got %x\nwant %x", bytesBack.Data, raw)
	}

	text, err := json.Marshal(WriteParams{Path: "/tmp/shot.png", Content: string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	var textBack WriteParams
	if err := json.Unmarshal(text, &textBack); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal([]byte(textBack.Content), raw) {
		t.Error("encoding/json no longer rewrites invalid UTF-8 in a string, which is the whole " +
			"reason MethodWriteBytes is a method and not a field on MethodWrite — re-read both")
	}
}
