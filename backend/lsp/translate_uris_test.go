package lsp

import "testing"

func TestTranslateURIsRoundTrip(t *testing.T) {
	mounts := map[string]string{"/opt/shared": "/shared"}
	const workspace = "/home/dev/checkout/app"

	cases := []struct {
		name        string
		in          string
		toContainer bool
		want        string
	}{
		{"workspace into container", "file://" + workspace + "/main.go", true, "file:///mnt/app/main.go"},
		{"project mount into container", "file:///opt/shared/lib.go", true, "file:///shared/lib.go"},
		{"container back to workspace", "file:///mnt/app/main.go", false, "file://" + workspace + "/main.go"},
		{"container mount back to host", "file:///shared/lib.go", false, "file:///opt/shared/lib.go"},
		// A path that repeats its own root keeps the repeats: only the leading root swaps.
		{"repeated root swaps once", "file://" + workspace + "/ws/app/x.go", true, "file:///mnt/app/ws/app/x.go"},
		{"foreign path untouched", "file:///etc/hosts", true, "file:///etc/hosts"},
		{"non-file uri untouched", "untitled:main.go", true, "untitled:main.go"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := translateURIs(tc.in, workspace, mounts, tc.toContainer); got != tc.want {
				t.Errorf("translateURIs(%q, toContainer=%v) = %v, want %v", tc.in, tc.toContainer, got, tc.want)
			}
		})
	}
}

func TestTranslateURIsRewritesNestedValues(t *testing.T) {
	const workspace = "/home/dev/checkout/app"
	data := map[string]any{
		"rootUri": "file://" + workspace,
		"docs":    []any{map[string]any{"uri": "file://" + workspace + "/a.go"}},
		"count":   2,
	}
	got := translateURIs(data, workspace, nil, true).(map[string]any)
	if got["rootUri"] != "file:///mnt/app" {
		t.Errorf("rootUri = %v", got["rootUri"])
	}
	inner := got["docs"].([]any)[0].(map[string]any)
	if inner["uri"] != "file:///mnt/app/a.go" {
		t.Errorf("nested uri = %v", inner["uri"])
	}
	if got["count"] != 2 {
		t.Errorf("non-string value changed: %v", got["count"])
	}
}
