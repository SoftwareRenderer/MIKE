package extensions

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"mike/pkg/sandboxruntime"
)

func TestFetchIndex(t *testing.T) {
	index := RegistryIndex{
		{
			Name:        "gopls",
			Version:     "1.0.0",
			Type:        ExtensionTypeLSP,
			DisplayName: "Go Language Server",
			Description: "Go language server",
			Author:      "Go Team",
			Image:       "ghcr.io/kanban/gopls:1.0.0",
		},
		{
			Name:        "typescript",
			Version:     "2.0.0",
			Type:        ExtensionTypeLSP,
			DisplayName: "TypeScript Language Server",
			Description: "TypeScript language server",
			Author:      "Microsoft",
			Image:       "ghcr.io/kanban/typescript:2.0.0",
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, index)
	}))
	defer server.Close()

	rc := NewRegistryClient()
	got, err := rc.FetchIndex(server.URL)
	if err != nil {
		t.Fatalf("FetchIndex: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Index length = %v, want 2", len(got))
	}
	if got[0].Name != "gopls" {
		t.Errorf("First entry name = %v, want gopls", got[0].Name)
	}
	if got[1].Name != "typescript" {
		t.Errorf("Second entry name = %v, want typescript", got[1].Name)
	}
}

func TestFetchIndex_FeaturedFlag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"name": "featured-ext", "version": "1.0.0", "type": "ui", "display_name": "Featured Ext", "description": "d", "author": "a", "featured": true},
			{"name": "plain-ext", "version": "1.0.0", "type": "ui", "display_name": "Plain Ext", "description": "d", "author": "a"}
		]`))
	}))
	defer server.Close()

	rc := NewRegistryClient()
	got, err := rc.FetchIndex(server.URL)
	if err != nil {
		t.Fatalf("FetchIndex: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("index length = %d, want 2", len(got))
	}
	if !got[0].Featured {
		t.Errorf("got[0].Featured = false, want true (featured: true)")
	}
	if got[1].Featured {
		t.Errorf("got[1].Featured = true, want false (field absent)")
	}

	// Round-trip: featured is emitted for true entries and omitted otherwise.
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(data), `"featured":true`) {
		t.Errorf("marshaled index missing \"featured\":true, got %s", data)
	}
	if strings.Count(string(data), `"featured"`) != 1 {
		t.Errorf("expected exactly one \"featured\" field (omitempty), got %s", data)
	}
}

// An index that cannot be read or parsed is an error, never the empty list the Browse panel would
// render as "nothing published" — which invites reinstalling extensions that are already installed.
func TestFetchIndexRefusesAnUnusableAnswer(t *testing.T) {
	notFound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer notFound.Close()
	malformed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("not json"))
	}))
	defer malformed.Close()

	rc := NewRegistryClient()
	for _, tc := range []struct {
		name string
		url  string
	}{
		{"http 404", notFound.URL},
		{"malformed json", malformed.URL},
		{"nothing listening", "http://localhost:1/not-a-real-server"},
		{"missing file", "file:///nonexistent/path/registry.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := rc.FetchIndex(tc.url); err == nil {
				t.Errorf("FetchIndex(%s) = nil, want an error", tc.name)
			}
		})
	}
}

// The Dockerfile a registry names reaches `podman build` as the bytes it published, on both
// transports: a rewrite of them changes what the image installs.
func TestFetchDockerfileReturnsTheBytesAsIs(t *testing.T) {
	rc := NewRegistryClient()

	const expected = "FROM ubuntu:latest\nRUN apt-get update && apt-get install -y gopls\nCMD [\"/usr/bin/gopls\", \"serve\"]\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(expected))
	}))
	defer server.Close()

	tmp := t.TempDir()
	dfPath := tmp + "/Dockerfile"
	content := "FROM ubuntu:22.04\nRUN apt-get update\n"
	if err := os.WriteFile(dfPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		url  string
		want string
	}{
		{"http", server.URL, expected},
		{"file", "file://" + dfPath, content},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := rc.FetchDockerfile(tc.url)
			if err != nil {
				t.Fatalf("FetchDockerfile(%s): %v", tc.name, err)
			}
			if got != tc.want {
				t.Errorf("Dockerfile = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRegistryIndexDecodesAPublishedEntry reads the registry text an install really fetches
// (../MIKE-extensions/registry.json) through the decoder: a json tag that drifted from the published
// field yields an entry with no repo, no commit and no subdir, so the checkout cannot be verified and
// the row can never be updated. The prose of an entry is the generator's business
// (`python3 scripts/gen-registry.py --check`); these tags are this package's.
func TestRegistryIndexDecodesAPublishedEntry(t *testing.T) {
	path, err := filepath.Abs(filepath.Join("..", "..", "..", "MIKE-extensions", "registry.json"))
	if err != nil {
		t.Fatalf("resolve the registry source: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the registry source beside this repo is unreadable: %v", err)
	}

	var index RegistryIndex
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatalf("registry.json does not decode into RegistryIndex: %v", err)
	}

	var found *RegistryEntry
	for i, entry := range index {
		if entry.Name == "typescript-language-server" {
			found = &index[i]
			break
		}
	}
	if found == nil {
		t.Fatal("registry.json has no typescript-language-server entry")
	}
	if found.Type != ExtensionTypeLSP {
		t.Errorf("Type = %q, want %q", found.Type, ExtensionTypeLSP)
	}
	if found.DisplayName == "" || found.Author == "" {
		t.Errorf("display_name/author did not decode: %+v", found)
	}
	if found.Repo == "" {
		t.Error("repo did not decode: an install would have nothing to clone")
	}
	if !commitHashRe.MatchString(found.Commit) {
		t.Errorf("commit %q is not a full hash: the checkout cannot be verified against it", found.Commit)
	}
	if found.Subdir != "lsp/typescript-language-server" {
		t.Errorf("Subdir = %q, want the directory an install materializes", found.Subdir)
	}
}

func TestNewEgressHosts(t *testing.T) {
	caps := func(hosts ...string) *Capabilities {
		return &Capabilities{Network: &NetworkConfig{AllowedDomains: hosts}}
	}
	tests := []struct {
		name      string
		installed *Capabilities
		remote    *Capabilities
		want      []string
	}{
		{"a new host is reported", caps("api.example.com"), caps("api.example.com", "telemetry.example.net"), []string{"telemetry.example.net"}},
		{"a lookalike subdomain is a new host", caps("example.com"), caps("evil.example.com"), []string{"evil.example.com"}},
		{"a subdomain of a granted host is a new host", caps("example.com"), caps("sub.example.com"), []string{"sub.example.com"}},
		{"a reword is a new host", caps("example.com"), caps("www.example.com"), []string{"www.example.com"}},
		{"a removed host is not a decision", caps("a.example.com", "b.example.com"), caps("a.example.com"), nil},
		{"every host removed is not a decision", caps("a.example.com"), caps(), nil},
		{"reordering is not a decision", caps("a.example.com", "b.example.com"), caps("b.example.com", "a.example.com"), nil},
		{"case and spacing are not a decision", caps("API.Example.com"), caps(" api.example.com "), nil},
		{"remote declares no network", caps("a.example.com"), &Capabilities{}, nil},
		{"both capabilities absent", nil, nil, nil},
		{"installed named none", nil, caps("b.example.com", "a.example.com"), []string{"a.example.com", "b.example.com"}},
		{"listed twice remotely is reported once", nil, caps("a.example.com", "A.EXAMPLE.COM"), []string{"a.example.com"}},
		{"blank entries are ignored", caps("a.example.com"), caps("a.example.com", "   "), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := newEgressHosts(tc.installed, tc.remote); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("newEgressHosts() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCompareVersions pins the ordering and the one decision taken from it: only a strictly newer
// remote version is an update. A non-semver pair falls back to lexicographic, which is why
// "1.0.0-alpha" outranks "1.0.0" — the answer an author gets when a pre-release looks newer.
func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b   string
		want   int
		update bool
	}{
		{"1.0.0", "1.0.0", 0, false},
		{"1.0.0", "1.0.1", -1, true},
		{"1.0.1", "1.0.0", 1, false},
		{"1.0.0", "2.0.0", -1, true},
		{"2.0.0", "1.0.0", 1, false},
		{"1.1.0", "1.0.9", 1, false},
		{"1.0.0", "1.1.0", -1, true},
		{"v1.0.0", "v1.0.0", 0, false},
		{"v1.0.0", "1.0.0", 0, false},
		{"1.0.0-alpha", "1.0.0", 1, false},
		{"latest", "latest", 0, false},
		{"beta", "alpha", 1, false},
	}

	for _, tt := range tests {
		t.Run(tt.a+"_vs_"+tt.b, func(t *testing.T) {
			if got := CompareVersion(tt.a, tt.b); got != tt.want {
				t.Errorf("CompareVersion(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
			if got := UpdateAvailable(tt.a, tt.b); got != tt.update {
				t.Errorf("UpdateAvailable(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.update)
			}
		})
	}
}

func TestFetchIndex_FileURL(t *testing.T) {
	rc := NewRegistryClient()

	// Create a temp file with valid registry JSON
	tmp := t.TempDir()
	indexPath := tmp + "/registry.json"
	data := `[{"name":"test-ext","version":"1.0.0","type":"lsp","display_name":"Test"}]`
	if err := os.WriteFile(indexPath, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	index, err := rc.FetchIndex("file://" + indexPath)
	if err != nil {
		t.Fatalf("FetchIndex(file://) = error: %v", err)
	}
	if len(index) != 1 {
		t.Fatalf("len(index) = %d, want 1", len(index))
	}
	if index[0].Name != "test-ext" {
		t.Fatalf("index[0].Name = %q, want %q", index[0].Name, "test-ext")
	}
}

// A locally built image lives under "localhost/", and only a qualified name keeps later
// exists/pull/create out of short-name resolution.
func TestExtensionImageRef(t *testing.T) {
	ref := ExtensionImageRef("gopls", "1.0.0")
	if ref != "localhost/kanban-ext-gopls:1.0.0" {
		t.Fatalf("ExtensionImageRef = %q, want localhost/kanban-ext-gopls:1.0.0", ref)
	}
	if !sandboxruntime.IsQualifiedImageRef(ref) {
		t.Fatalf("extension image ref %q is not fully qualified", ref)
	}
	if err := sandboxruntime.ValidateConfiguredImageRef(ref); err != nil {
		t.Fatalf("extension image ref %q rejected: %v", ref, err)
	}
}

// A registry entry's image must name its registry — it is pulled headless.
func TestRegistryClientValidateImage(t *testing.T) {
	rc := NewRegistryClient()
	for _, ok := range []string{"", "docker.io/library/ubuntu:latest", "localhost/x:1", "localhost:5000/o/r"} {
		if err := rc.ValidateImage(ok); err != nil {
			t.Errorf("ValidateImage(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"ubuntu:latest", "alpine/git", "kanban-ext-gopls:1.0.0"} {
		if err := rc.ValidateImage(bad); err == nil {
			t.Errorf("ValidateImage(%q) = nil, want a fully-qualified-name error", bad)
		}
	}
}

// makeGitRepo creates a local SHA-256 git repo with the given files (path →
// content), commits them, and returns (repoPath, commitSHA).
func makeGitRepo(t *testing.T, files map[string]string) (string, string) {
	return makeGitRepoFormat(t, files, "sha256")
}

// makeGitRepoFormat creates a local git repo in the given object format ("sha1",
// "sha256", or "" for the default) with the given files (path → content),
// commits them, and returns (repoPath, commitSHA).
func makeGitRepoFormat(t *testing.T, files map[string]string, objectFormat string) (string, string) {
	t.Helper()
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	initArgs := []string{"init", "-q", "-b", "main"}
	if objectFormat != "" {
		initArgs = append(initArgs, "--object-format="+objectFormat)
	}
	run(initArgs...)
	for path, content := range files {
		full := filepath.Join(repo, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "-A")
	run("commit", "-qm", "init")
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	return repo, strings.TrimSpace(string(out))
}

func TestCheckoutExtension_Success(t *testing.T) {
	repo, commit := makeGitRepo(t, map[string]string{
		"myext/manifest.json": `{"display_name":"X","version":"1.0.0"}`,
		"myext/Dockerfile":    "FROM ubuntu\n",
	})
	dest := filepath.Join(t.TempDir(), "ui", "x")
	rc := NewRegistryClient()
	if err := rc.CheckoutExtension(repo, commit, "myext", dest); err != nil {
		t.Fatalf("CheckoutExtension: %v", err)
	}
	for _, f := range []string{"manifest.json", "Dockerfile"} {
		if _, err := os.Stat(filepath.Join(dest, f)); err != nil {
			t.Errorf("%s not materialized: %v", f, err)
		}
	}
	// Content must match the committed bytes.
	data, err := os.ReadFile(filepath.Join(dest, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"display_name":"X"`) {
		t.Errorf("manifest content mismatch: %s", data)
	}
}

func TestCheckoutExtension_SubdirEmpty_WholeRepo(t *testing.T) {
	repo, commit := makeGitRepo(t, map[string]string{
		"manifest.json": `{"display_name":"X"}`,
	})
	dest := filepath.Join(t.TempDir(), "agent", "x")
	rc := NewRegistryClient()
	if err := rc.CheckoutExtension(repo, commit, "", dest); err != nil {
		t.Fatalf("CheckoutExtension: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "manifest.json")); err != nil {
		t.Errorf("manifest.json not materialized: %v", err)
	}
}

func TestCheckoutExtension_VerifyRejectsMissingCommit(t *testing.T) {
	repo, _ := makeGitRepo(t, map[string]string{
		"myext/manifest.json": `{"display_name":"X"}`,
	})
	dest := filepath.Join(t.TempDir(), "ui", "x")
	rc := NewRegistryClient()
	// Valid-format SHA-256 that is not reachable from any ref → checkout must
	// fail and nothing may be installed.
	missing := strings.Repeat("deadbeef", 8) // 64-char hex, not present in repo
	err := rc.CheckoutExtension(repo, missing, "myext", dest)
	if err == nil {
		t.Fatal("CheckoutExtension(unreachable commit) = nil, want error")
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Error("dest dir was created despite failed checkout")
	}
}

func TestCheckoutExtension_SHA1Commit(t *testing.T) {
	// A 40-char SHA-1 commit from a SHA-1 repo must install successfully.
	repo, sha1 := makeGitRepoFormat(t, map[string]string{
		"myext/manifest.json": `{"display_name":"X"}`,
		"myext/Dockerfile":    "FROM ubuntu\n",
	}, "sha1")
	if len(sha1) != 40 {
		t.Fatalf("expected SHA-1 repo to yield 40-char hash, got %d (%s)", len(sha1), sha1)
	}
	dest := filepath.Join(t.TempDir(), "ui", "x")
	rc := NewRegistryClient()
	if err := rc.CheckoutExtension(repo, sha1, "myext", dest); err != nil {
		t.Fatalf("CheckoutExtension(SHA-1 commit): %v", err)
	}
	for _, f := range []string{"manifest.json", "Dockerfile"} {
		if _, err := os.Stat(filepath.Join(dest, f)); err != nil {
			t.Errorf("%s not materialized: %v", f, err)
		}
	}
}

func TestCheckoutExtension_ClearsExistingDest(t *testing.T) {
	// Re-checking out a new vetted revision must clear the previous install so
	// files removed in the new version do not linger.
	repoOld, oldCommit := makeGitRepo(t, map[string]string{
		"myext/manifest.json": `{"display_name":"Old"}`,
		"myext/stale.txt":     "gone",
	})
	repoNew, newCommit := makeGitRepo(t, map[string]string{
		"myext/manifest.json": `{"display_name":"New"}`,
		"myext/kept.txt":      "here",
	})
	dest := filepath.Join(t.TempDir(), "ui", "x")
	rc := NewRegistryClient()
	if err := rc.CheckoutExtension(repoOld, oldCommit, "myext", dest); err != nil {
		t.Fatalf("CheckoutExtension(old): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "stale.txt")); err != nil {
		t.Fatalf("setup: stale.txt not materialized: %v", err)
	}
	if err := rc.CheckoutExtension(repoNew, newCommit, "myext", dest); err != nil {
		t.Fatalf("CheckoutExtension(new): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "kept.txt")); err != nil {
		t.Errorf("kept.txt not materialized: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "stale.txt")); err == nil {
		t.Error("stale.txt still present after re-checkout; old target was not cleared")
	}
}

func TestCheckoutExtension_InvalidInput(t *testing.T) {
	rc := NewRegistryClient()
	dest := t.TempDir()
	tests := []struct {
		name, repo, commit, subdir string
	}{
		{"repo dash prefix", "-evil", strings.Repeat("a", 64), ""},
		{"commit not hex", "https://example.com/r", "not-a-sha", ""},
		{"commit short sha", "https://example.com/r", "abc123", ""},
		{"commit bad length", "https://example.com/r", strings.Repeat("a", 41), ""},
		{"subdir traversal", "https://example.com/r", strings.Repeat("a", 64), "../.."},
		{"subdir absolute", "https://example.com/r", strings.Repeat("a", 64), "/etc"},
		{"empty repo", "", strings.Repeat("a", 64), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := rc.CheckoutExtension(tt.repo, tt.commit, tt.subdir, dest); err == nil {
				t.Errorf("CheckoutExtension(%q) = nil, want error", tt.name)
			}
		})
	}
}

func TestCopyDirSkipsDotGit(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "ext")

	// Repo root containing an extension file plus a .git directory.
	if err := os.WriteFile(filepath.Join(src, "manifest.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitDir := filepath.Join(src, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "objects", "pack"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "objects", "pack", "pack-abc.pack"), []byte("objects"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := copyDir(src, dst); err != nil {
		t.Fatalf("copyDir: %v", err)
	}

	// The extension file must be present, the .git dir must be absent.
	if _, err := os.Stat(filepath.Join(dst, "manifest.json")); err != nil {
		t.Errorf("manifest.json not copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); !os.IsNotExist(err) {
		t.Errorf("expected .git to be skipped, err=%v", err)
	}
}

func TestResolveURL(t *testing.T) {
	tests := []struct {
		base   string
		target string
		want   string
	}{
		// file:// base with relative path (registry in extensions/ dir, target is gopls/manifest.json)
		{"file:///home/user/extensions/registry.json", "gopls/manifest.json", "file:///home/user/extensions/gopls/manifest.json"},
		{"file:///home/user/extensions/registry.json", "./gopls/manifest.json", "file:///home/user/extensions/gopls/manifest.json"},
		// Absolute file:// target returns as-is
		{"file:///base/registry.json", "file:///absolute/path.json", "file:///absolute/path.json"},
		// HTTP base with relative path
		{"https://registry.example.com/index.json", "gopls/manifest.json", "https://registry.example.com/gopls/manifest.json"},
		{"https://registry.example.com/v1/index.json", "../gopls/manifest.json", "https://registry.example.com/gopls/manifest.json"},
		// Absolute HTTP target returns as-is
		{"https://base.com/index.json", "https://other.com/manifest.json", "https://other.com/manifest.json"},
		// Target with scheme returns as-is
		{"file:///base/registry.json", "https://remote.com/manifest.json", "https://remote.com/manifest.json"},
	}

	for _, tt := range tests {
		got := ResolveURL(tt.base, tt.target)
		if got != tt.want {
			t.Errorf("ResolveURL(%q, %q) = %q, want %q", tt.base, tt.target, got, tt.want)
		}
	}
}

func TestRegistryClient_DoesNotFollowRedirects(t *testing.T) {
	// The registry client must not follow a redirect from the registry URL to
	// an arbitrary internal host (SSRF via redirect).
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"name":"internal"}]`))
	}))
	defer internal.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL, http.StatusFound)
	}))
	defer redirector.Close()

	rc := NewRegistryClient()
	_, err := rc.FetchIndex(redirector.URL)
	if err == nil {
		t.Fatal("expected error fetching a redirecting registry URL, got nil")
	}
	if strings.Contains(err.Error(), "internal") {
		t.Fatalf("registry client followed a redirect: %v", err)
	}
}

func TestFetchChangelog_URLDerivation(t *testing.T) {
	// The changelog is fetched from <registry-base>/<author-slug>/<name> —
	// the same base location that serves the registry index.
	const changelog = `{"hash":"aaa","version":"1.0.0","description":"x"}`
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.URL.Path == "/exts/microsoft/typescript-language-server" {
			w.Write([]byte(changelog))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	rc := NewRegistryClient()
	data, err := rc.FetchChangelog(server.URL+"/exts/registry.json", "Microsoft", "typescript-language-server")
	if err != nil {
		t.Fatalf("FetchChangelog: %v", err)
	}
	if gotPath != "/exts/microsoft/typescript-language-server" {
		t.Errorf("requested path = %q, want /exts/microsoft/typescript-language-server", gotPath)
	}
	if data != changelog {
		t.Errorf("data = %q, want %q", data, changelog)
	}
}

func TestFetchChangelog_FileBase(t *testing.T) {
	// file:// registry base (local-registry workflow): the changelog file
	// lives next to the registry index file.
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "registry.json"), []byte(`[]`), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "go-team")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const changelog = `{"hash":"aaa","version":"1.0.0","description":"x"}`
	if err := os.WriteFile(filepath.Join(dir, "gopls"), []byte(changelog), 0o644); err != nil {
		t.Fatal(err)
	}

	rc := NewRegistryClient()
	data, err := rc.FetchChangelog("file://"+filepath.Join(base, "registry.json"), "Go Team", "gopls")
	if err != nil {
		t.Fatalf("FetchChangelog: %v", err)
	}
	if data != changelog {
		t.Errorf("data = %q, want %q", data, changelog)
	}
}

func TestFetchChangelog_Missing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	rc := NewRegistryClient()
	if _, err := rc.FetchChangelog(server.URL+"/exts/registry.json", "Microsoft", "missing"); err == nil {
		t.Fatal("expected error fetching a missing changelog, got nil")
	}
}

// --- Extension checkout: clone-source hardening ------------------------------
//
// The extension checkout clones registry content with the host's git, and the
// clone's output goes into the install error. Without the shared clone gate, a
// hostile index entry (`repo: "ext::sh -c 'env >&2'"`) would run a shell as the
// server user — with MIKE_ENCRYPTION_KEY in its
// environment — and echo it back to the client.

func TestValidateGitSourceRejectsHostileRepos(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	tests := []struct {
		name    string
		repo    string
		commit  string
		wantErr bool
	}{
		{"https repo", "https://github.com/author/ext.git", sha, false},
		{"ssh repo", "git@github.com:author/ext.git", sha, false},
		{"scp-like no user", "github.com:author/ext.git", sha, false},
		{"ext helper shell", "ext::sh -c 'id'", sha, true},
		{"ext helper touch", "ext::touch /tmp/kanban-ext-pwn", sha, true},
		{"fd helper", "fd::11", sha, true},
		{"python helper", "python::Foo", sha, true},
		{"flag injection upload-pack", "--upload-pack=touch /tmp/pwn", sha, true},
		{"flag injection reference", "--reference=/etc", sha, true},
		{"uppercase scheme", "HTTPS://example.com/r.git", sha, true}, // git's check is case-sensitive
		{"disallowed scheme", "smb://host/share/repo", sha, true},
		{"newline in repo", "https://example.com/r.git\n--upload-pack=x", sha, true},
		{"nul in repo", "https://example.com/r.git\x00", sha, true},
		{"empty repo", "", sha, true},
		// The pinned commit must be exactly a full hash.
		{"short commit", "https://example.com/r.git", "0123456", true},
		{"hash with payload", "https://example.com/r.git", sha + "; rm -rf /", true},
		{"64-char sha-256 hash", "https://example.com/r.git", sha + sha[:24], false},
		{"80-char hash", "https://example.com/r.git", sha + sha, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateGitSource(tt.repo, tt.commit)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateGitSource(%q, %q) = %v, wantErr %v", tt.repo, tt.commit, err, tt.wantErr)
			}
		})
	}
}

// TestCheckoutExtensionHelperTransportNeverExecutes: a hostile index entry is
// refused and its payload never runs (no canary file), whatever the host's
// protocol.ext.allow setting says.
func TestCheckoutExtensionHelperTransportNeverExecutes(t *testing.T) {
	rc := NewRegistryClient()
	tmp := t.TempDir()
	canary := filepath.Join(tmp, "pwned")
	const sha = "0123456789abcdef0123456789abcdef01234567"

	if err := rc.CheckoutExtension("ext::sh -c 'touch "+canary+"'", sha, "", filepath.Join(tmp, "dest")); err == nil {
		t.Fatal("hostile clone source accepted")
	}
	if _, err := os.Stat(canary); err == nil {
		t.Fatal("ext:: payload executed — the clone source was not validated")
	}

	// A dash-leading source is refused the same way.
	if err := rc.CheckoutExtension("--upload-pack=touch "+canary, sha, "", filepath.Join(tmp, "dest")); err == nil {
		t.Error("flag-shaped clone source accepted")
	}
	if _, err := os.Stat(canary); err == nil {
		t.Fatal("flag payload executed")
	}
}

// TestExtCloneArgsFencesTransports: the protocol fence must precede the `clone`
// subcommand, so it reaches the process that parses the source URL.
func TestExtCloneArgsFencesTransports(t *testing.T) {
	repo := "https://example.com/author/ext.git"
	args := extCloneArgs(repo, "/tmp/dst")

	cloneIdx := -1
	for i, a := range args {
		if a == "clone" {
			cloneIdx = i
			break
		}
	}
	if cloneIdx < 1 {
		t.Fatalf("no clone subcommand in %v", args)
	}
	fenced := false
	for _, a := range args[:cloneIdx] {
		if a == "protocol.ext.allow=never" {
			fenced = true
		}
	}
	if !fenced {
		t.Errorf("protocol.ext.allow=never missing or set after `clone`: %v", args)
	}
	if args[len(args)-2] != repo {
		t.Errorf("source must be an unmodified single argument, got %v", args)
	}
}
