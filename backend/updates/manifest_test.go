package updates

import (
	"encoding/json"
	"strings"
	"testing"
)

// manifestDoc builds a manifest body with the handful of fields a test is arguing about.
func manifestDoc(t *testing.T, fields map[string]any) []byte {
	t.Helper()
	body := map[string]any{
		"schema": SchemaVersion,
		"latest": "1.4.2",
		"assets": map[string]any{
			"linux/amd64": map[string]any{
				"url":    "https://getmike.dev/download/mike-server-1.4.2-linux-amd64.tar.gz",
				"sha256": strings.Repeat("a", 64),
			},
		},
	}
	for k, v := range fields {
		if v == nil {
			delete(body, k)
			continue
		}
		body[k] = v
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

const hosted = "https://getmike.dev/version.json"

func TestParseManifestAcceptsAWellFormedIndex(t *testing.T) {
	m, err := ParseManifest(manifestDoc(t, map[string]any{"latest": "v1.4.2"}), hosted)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	// The leading "v" is a tag spelling, not part of the version: everything downstream compares
	// dotted numbers.
	if m.Latest != "1.4.2" {
		t.Errorf("Latest = %q, want 1.4.2", m.Latest)
	}
	asset, ok := m.AssetFor("linux", "amd64")
	if !ok {
		t.Fatalf("no asset for linux/amd64: %v", m.Assets)
	}
	if asset.SHA256 != strings.Repeat("a", 64) {
		t.Errorf("digest = %q", asset.SHA256)
	}
	if _, ok := m.AssetFor("linux", "arm64"); ok {
		t.Errorf("an asset the manifest never named was returned")
	}
}

// Every refusal here is a manifest that would otherwise offer the wrong bytes: a schema this build does
// not understand, a version that cannot be compared, or an archive from a host nobody chose.
func TestParseManifestRefusals(t *testing.T) {
	otherHost := map[string]any{
		"assets": map[string]any{
			"linux/amd64": map[string]any{
				"url":    "https://elsewhere.example/mike.tar.gz",
				"sha256": strings.Repeat("b", 64),
			},
		},
	}
	plainHTTP := map[string]any{
		"assets": map[string]any{
			"linux/amd64": map[string]any{
				"url":    "http://getmike.dev/mike.tar.gz",
				"sha256": strings.Repeat("b", 64),
			},
		},
	}
	cases := []struct {
		name   string
		fields map[string]any
		from   string
		want   string
	}{
		{name: "not JSON", fields: nil, from: hosted, want: "not a JSON object"},
		{name: "no schema", fields: map[string]any{"schema": nil}, from: hosted, want: `"schema"`},
		{name: "newer schema", fields: map[string]any{"schema": SchemaVersion + 1}, from: hosted, want: "schema 2 is not the schema 1"},
		{name: "no latest", fields: map[string]any{"latest": nil}, from: hosted, want: `"latest"`},
		{name: "latest is not dotted", fields: map[string]any{"latest": "1.4.2-rc1"}, from: hosted, want: "not a dotted version"},
		{name: "no assets", fields: map[string]any{"assets": nil}, from: hosted, want: `"assets"`},
		{name: "asset on another host", fields: otherHost, from: hosted, want: "elsewhere.example"},
		{name: "asset over plain http", fields: plainHTTP, from: hosted, want: "https"},
		{name: "asset digest too short", fields: map[string]any{"assets": map[string]any{"linux/amd64": map[string]any{"url": "https://getmike.dev/a.tar.gz", "sha256": "abc"}}}, from: hosted, want: "64 lowercase hex"},
		{name: "asset key is not os/arch", fields: map[string]any{"assets": map[string]any{"amd64": map[string]any{"url": "https://getmike.dev/a.tar.gz", "sha256": strings.Repeat("c", 64)}}}, from: hosted, want: "os/arch"},
		{name: "manifest URL has no host", fields: nil, from: "/local/path/version.json", want: "no host"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body []byte
			if tc.name == "not JSON" {
				body = []byte("[1,2,3]")
			} else {
				body = manifestDoc(t, tc.fields)
			}
			_, err := ParseManifest(body, tc.from)
			if err == nil {
				t.Fatalf("ParseManifest accepted a %s manifest", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not say %q", err, tc.want)
			}
		})
	}

	t.Run("an oversized body is refused before it is parsed", func(t *testing.T) {
		pad := strings.Repeat("x", maxManifestBytes+1)
		if _, err := ParseManifest([]byte(pad), hosted); err == nil || !strings.Contains(err.Error(), "over the") {
			t.Fatalf("oversized manifest: %v", err)
		}
	})

	t.Run("loopback matches loopback", func(t *testing.T) {
		body := manifestDoc(t, map[string]any{
			"assets": map[string]any{
				"linux/amd64": map[string]any{
					"url":    "http://127.0.0.1:9999/mike.tar.gz",
					"sha256": strings.Repeat("d", 64),
				},
			},
		})
		if _, err := ParseManifest(body, "http://127.0.0.1:9999/version"); err != nil {
			t.Fatalf("a local fixture should parse: %v", err)
		}
	})
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b    string
		want    int
		wantErr bool
	}{
		{a: "1.4.2", b: "1.4.2", want: 0},
		{a: "1.4", b: "1.4.0", want: 0},
		{a: "1.4.1", b: "1.4.2", want: -1},
		{a: "1.4.2", b: "1.4.1", want: 1},
		// The reason this is not a string compare: "1.4.10" sorts before "1.4.9" as text.
		{a: "1.4.10", b: "1.4.9", want: 1},
		{a: "1.10.0", b: "1.9.9", want: 1},
		{a: "v2.0.0", b: "1.99.99", want: 1},
		{a: "0.0.0", b: "0.0.1", want: -1},
		{a: "dev", b: "1.0.0", wantErr: true},
		{a: "1.0.0", b: "compose", wantErr: true},
		{a: "1.0.0-rc1", b: "1.0.0", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.a+"_vs_"+tc.b, func(t *testing.T) {
			got, err := CompareVersions(tc.a, tc.b)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("CompareVersions(%q,%q) = %d, want an error", tc.a, tc.b, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("CompareVersions: %v", err)
			}
			if got != tc.want {
				t.Errorf("CompareVersions(%q,%q) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// "Cannot tell" is a third answer, and the dot and the buttons read it differently from both "current"
// and "newer available".
func TestKnownVersion(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"1.4.2", true},
		{"v1.4.2", true},
		{"0.0.0", true},
		{"dev", false},
		{"docker", false},
		{"compose", false},
		{"", false},
		{"v1.4.2-4-gabcdef", false},
		{"1.4.2-dirty", false},
	} {
		if got := KnownVersion(tc.value); got != tc.want {
			t.Errorf("KnownVersion(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestNewRefusesAURLItCannotFetch(t *testing.T) {
	for _, raw := range []string{"http://getmike.dev/version.json", "not a url", "https:///no-host"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := New(Options{ManifestURL: raw, Version: "1.0.0"}); err == nil {
				t.Fatalf("New accepted %q", raw)
			} else if !strings.Contains(err.Error(), "manifest URL") {
				t.Errorf("error %q does not name the manifest URL", err)
			}
		})
	}
	t.Run("the hosted default is what an empty decision means", func(t *testing.T) {
		s, err := New(Options{Version: "1.0.0"})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if got := s.Status(Settings{}).ManifestURL; got != DefaultManifestURL {
			t.Errorf("ManifestURL = %q, want %q", got, DefaultManifestURL)
		}
	})
}

func TestStatusAnswersWithoutACheck(t *testing.T) {
	s, err := New(Options{Version: "1.4.0", InstallDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	st := s.Status(Settings{Enabled: true, Answered: true})
	if st.State.Latest != "" || st.State.Available || st.State.CheckedAt != "" {
		t.Errorf("a service that never checked answered as if it had: %+v", st.State)
	}
	if st.State.CurrentVersion != "1.4.0" {
		t.Errorf("CurrentVersion = %q", st.State.CurrentVersion)
	}
	if !st.Enabled || st.Disabled || !st.Answered {
		t.Errorf("the settings did not reach the answer: %+v", st)
	}
	if st.Working != "" {
		t.Errorf("Working = %q with nothing running", st.Working)
	}
}
