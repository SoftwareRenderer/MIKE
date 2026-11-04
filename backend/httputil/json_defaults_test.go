package httputil

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"testing"
	"time"
)

// The wire contract of encoding/json/v2 run with its own defaults, which is what every request and
// response in this server is made of. Each row below is a shape a client or a provider acts on, so a
// default reached for at one call site shows up here as a changed byte rather than as a surprise in
// the browser. docs/api-contract.md → The JSON era this server speaks.

// wireShape holds the shapes MIKE actually sends: a list that may not have been loaded, a map that may
// not have been filled, a value forwarded without being understood, and a row's instant.
type wireShape struct {
	Items  []string          `json:"items"`
	Labels map[string]string `json:"labels"`
	Raw    jsontext.Value    `json:"raw"`
	At     time.Time         `json:"at"`
}

func TestMarshalFormatsAbsentAsEmptyAndLeavesForeignTextAlone(t *testing.T) {
	at := time.Date(2026, 11, 4, 9, 8, 7, 500000000, time.UTC)

	tests := []struct {
		name  string
		value any
		want  string
		opts  []json.Options
	}{
		{
			name:  "an unloaded list is an empty array, so a client that counts it reads zero rather than reaching for a fallback",
			value: wireShape{Items: nil, Labels: nil, At: at},
			want:  `{"items":[],"labels":{},"raw":null,"at":"2026-11-04T09:08:07.5Z"}`,
		},
		{
			name:  "bytes that were never there are the empty string, so whether a payload exists is not readable off the response",
			value: struct{ Body []byte }{Body: nil},
			want:  `{"Body":""}`,
		},
		{
			name:  "the bytes of a file stay base64, the one v1 formatting a plain []byte shared",
			value: struct{ B []byte }{B: []byte{1, 2, 3}},
			want:  `{"B":"AQID"}`,
		},
		{
			name:  "markup reaches the client as itself; the sanitizer, not the encoder, decides what is safe",
			value: map[string]string{"q": "<a> & </a>"},
			want:  `{"q":"<a> & </a>"}`,
		},
		{
			name:  "a line separator stays one character, because no response is ever pasted into a JavaScript literal",
			value: map[string]string{"q": "a\u2028b\u2029c"},
			want:  "{\"q\":\"a\u2028b\u2029c\"}",
		},
		{
			name:  "a forwarded payload is written exactly as it arrived",
			value: struct{ R jsontext.Value }{R: jsontext.Value(`{"z":1,"a":[2]}`)},
			want:  `{"R":{"z":1,"a":[2]}}`,
		},
		{
			name:  "an instant drops trailing fractional zeros, which is the text the rows store",
			value: map[string]time.Time{"t": at},
			want:  `{"t":"2026-11-04T09:08:07.5Z"}`,
		},
		{
			name:  "two-space indentation is an option, not a separate function",
			value: map[string]string{"k": "v"},
			want:  "{\n  \"k\": \"v\"\n}",
			opts:  []json.Options{jsontext.WithIndent("  ")},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.value, tc.opts...)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("marshal gave %s, want %s", got, tc.want)
			}
		})
	}
}

func TestMarshalRefusesBytesNoClientShouldBeGiven(t *testing.T) {
	out, err := json.Marshal(map[string]string{"q": "ab\xc3\x28"})
	if err == nil {
		t.Fatalf("marshaling invalid UTF-8 returned %s and no error; the byte rule is that it does not", out)
	}
	if jsontext.Value(out).IsValid() {
		t.Errorf("the bytes returned beside the error (%s) are valid JSON, so a caller could write them by mistake", out)
	}
}

// TestMarshalDoesNotOrderMapKeys is the reason no comparison of marshaled map JSON may read its bytes:
// the encoder's key order is unspecified, and the option that sorts them is opt-in per call.
func TestMarshalDoesNotOrderMapKeys(t *testing.T) {
	sorted, err := json.Marshal(map[string]int{"z": 1, "a": 2, "m": 3}, json.Deterministic(true))
	if err != nil {
		t.Fatalf("deterministic marshal: %v", err)
	}
	if string(sorted) != `{"a":2,"m":3,"z":1}` {
		t.Errorf("Deterministic(true) gave %s, want the keys sorted", sorted)
	}
}

func TestUnmarshalAcceptsOnlyWhatTheGrammarAllows(t *testing.T) {
	type request struct {
		Title string         `json:"title"`
		Loose string         `json:"loose,case:ignore"`
		Args  jsontext.Value `json:"args"`
		Count any            `json:"count"`
	}

	t.Run("a member whose case does not match a field is dropped, and the field keeps its zero value", func(t *testing.T) {
		var got request
		if err := json.Unmarshal([]byte(`{"TITLE":"x"}`), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.Title != "" {
			t.Fatalf("TITLE filled Title (%q): matching is meant to be on the exact name", got.Title)
		}
	})

	t.Run("a field may ask for the looser match on its own", func(t *testing.T) {
		var got request
		if err := json.Unmarshal([]byte(`{"LOOSE":"y"}`), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.Loose != "y" {
			t.Errorf("case:ignore did not match LOOSE, got %q", got.Loose)
		}
	})

	t.Run("a member named twice is refused rather than resolved to the last one", func(t *testing.T) {
		var got request
		if err := json.Unmarshal([]byte(`{"title":"a","title":"b"}`), &got); err == nil {
			t.Errorf("duplicate members unmarshaled to %q and no error", got.Title)
		}
		var m map[string]int
		if err := json.Unmarshal([]byte(`{"a":1,"a":2}`), &m); err == nil {
			t.Errorf("duplicate members unmarshaled a map of %v and no error", m)
		}
	})

	t.Run("a string holding bytes no decoder can name is refused", func(t *testing.T) {
		var got request
		body := append([]byte(`{"title":"ab`), 0xff, 0xfe)
		body = append(body, []byte(`"}`)...)
		if err := json.Unmarshal(body, &got); err == nil {
			t.Errorf("invalid UTF-8 unmarshaled to %q and no error", got.Title)
		}
	})

	t.Run("bytes after the value are refused, so a body cannot hold two requests", func(t *testing.T) {
		var got request
		if err := json.Unmarshal([]byte(`{"title":"a"} {"title":"b"}`), &got); err == nil {
			t.Errorf("a second top-level value unmarshaled to %q and no error", got.Title)
		}
	})

	t.Run("a member nothing names is ignored, which is what lets a provider add fields", func(t *testing.T) {
		var got request
		if err := json.Unmarshal([]byte(`{"title":"a","a_new_provider_field":1}`), &got); err != nil {
			t.Fatalf("an unknown member failed the decode: %v", err)
		}
		if got.Title != "a" {
			t.Errorf("title = %q, want a", got.Title)
		}
	})

	t.Run("a number under any is a float64, so a count read back out of it is formatted, not carried", func(t *testing.T) {
		var got request
		if err := json.Unmarshal([]byte(`{"count":1234567}`), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if _, ok := got.Count.(float64); !ok {
			t.Fatalf("count arrived as %T, not float64", got.Count)
		}
	})

	t.Run("a raw member keeps its nested value whole", func(t *testing.T) {
		var got request
		if err := json.Unmarshal([]byte(`{"args":{"path":"a/b"}}`), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if string(got.Args) != `{"path":"a/b"}` {
			t.Errorf("raw args = %s, want the object as it arrived", got.Args)
		}
	})
}
