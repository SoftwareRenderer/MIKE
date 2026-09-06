package extensions

// Tests for the one field this feature asks of the user: a host directory. Everything downstream
// follows from the string typed into it, so these pin what is accepted, what is refused with a
// readable sentence, and what a manifest is never allowed to supply.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateDependencyGrant(t *testing.T) {
	dir := t.TempDir()

	// The accepted case, in the shape a person actually types it: a trailing slash and a
	// redundant segment that cleaning takes care of.
	got, err := ValidateDependencyGrant(dir + "/./")
	if err != nil {
		t.Fatalf("a real directory must be accepted: %v", err)
	}
	if got != filepath.Clean(dir) {
		t.Errorf("grant = %q, want the cleaned %q", got, filepath.Clean(dir))
	}

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"blank", "   ", "no directory given"},
		{"relative", "go/pkg/mod", "not an absolute path"},
		{"parent escape", "/home/user/go/../etc", "must not contain '..'"},
		{"the whole machine", "/", "not a dependency directory"},
		{"does not exist", filepath.Join(dir, "not-there"), "cannot be read"},
		{"not a directory", filepath.Join(dir, "a-file"), "is not a directory"},
	}
	if err := os.WriteFile(filepath.Join(dir, "a-file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ValidateDependencyGrant(tc.in); err == nil {
				t.Fatalf("accepted %q", tc.in)
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestValidateDependencyGrant_StoresTheSymlinkTarget(t *testing.T) {
	// Storing the link's own path would let the grant widen after the fact: whoever can write the
	// link can retarget it, and the mount would follow without anyone granting that. Resolving
	// first means the path in the record is the path that gets bound, and is what the UI shows.
	outside := t.TempDir()
	root := t.TempDir()
	link := filepath.Join(root, "cache")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}

	got, err := ValidateDependencyGrant(link)
	if err != nil {
		t.Fatalf("a link to a real directory must be accepted: %v", err)
	}
	if got != filepath.Clean(outside) {
		t.Errorf("grant = %q, want the resolved target %q", got, filepath.Clean(outside))
	}
}

// lspExtWithCache is an installed LSP extension of the shape task 2 produces: the declaration
// present, no grant yet.
func lspExtWithCache(t *testing.T, store *ExtensionStore) *Extension {
	t.Helper()
	ext := &Extension{
		ID:      "go-team-gopls-test",
		Name:    "gopls",
		Type:    "lsp",
		Enabled: true,
		SandboxConfig: SandboxConfig{
			Dependency: &DependencyCache{ContainerPath: "/go/pkg/mod", Env: "GOMODCACHE"},
		},
	}
	ext.Manifest.Capabilities = &Capabilities{LSP: &LSPConfig{
		FileTypes: []LSPFileType{{Extension: "go", LanguageID: "go"}},
	}}
	if err := store.Create(ext); err != nil {
		t.Fatalf("create extension: %v", err)
	}
	return ext
}

func putDependencyCache(t *testing.T, h *ExtensionHandler, id, hostPath string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"host_path": hostPath})
	req := httptest.NewRequest(http.MethodPut, "/api/extensions/"+id+"/dependency-cache", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleSetDependencyCache(rec, req, id)
	return rec
}

func TestHandleSetDependencyCache_GrantsAndRevokes(t *testing.T) {
	h, store := testHandlerWithStore(t)
	ext := lspExtWithCache(t, store)
	dir := t.TempDir()

	rec := putDependencyCache(t, h, ext.ID, dir)
	if rec.Code != http.StatusOK {
		t.Fatalf("grant = %d %s, want 200", rec.Code, rec.Body)
	}
	stored, err := store.GetByID(ext.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SandboxConfig.DependencyHostPath != filepath.Clean(dir) {
		t.Errorf("stored grant = %q, want %q", stored.SandboxConfig.DependencyHostPath, filepath.Clean(dir))
	}
	// The declaration survives the write — a grant that overwrote the manifest's location would
	// leave a cache mounted at nothing.
	if stored.SandboxConfig.Dependency == nil || stored.SandboxConfig.Dependency.ContainerPath != "/go/pkg/mod" {
		t.Errorf("declaration damaged by the grant: %+v", stored.SandboxConfig.Dependency)
	}
	// The answer has to carry both halves, or the UI shows a field with no state.
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	sb, _ := resp["sandbox_config"].(map[string]any)
	if sb["dependency_host_path"] != filepath.Clean(dir) {
		t.Errorf("response sandbox_config = %v, want dependency_host_path reported back", sb)
	}
	if _, ok := sb["dependency_cache"].(map[string]any); !ok {
		t.Errorf("response sandbox_config = %v, want the declaration in it too", sb)
	}

	rec = putDependencyCache(t, h, ext.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke = %d %s, want 200", rec.Code, rec.Body)
	}
	stored, _ = store.GetByID(ext.ID)
	if stored.SandboxConfig.DependencyHostPath != "" {
		t.Errorf("after revoke the grant is still %q", stored.SandboxConfig.DependencyHostPath)
	}
}

func TestHandleSetDependencyCache_RejectsWhatIsNotAGrant(t *testing.T) {
	h, store := testHandlerWithStore(t)
	ext := lspExtWithCache(t, store)

	// A path that does not exist yet is the common mistake, and a container that fails to build is
	// not how it should surface.
	if rec := putDependencyCache(t, h, ext.ID, "/nonexistent/mod-cache"); rec.Code != http.StatusBadRequest {
		t.Errorf("missing directory = %d, want 400", rec.Code)
	}
	if rec := putDependencyCache(t, h, ext.ID, "relative/path"); rec.Code != http.StatusBadRequest {
		t.Errorf("relative path = %d, want 400", rec.Code)
	}
	if _, err := store.GetByID(ext.ID); err != nil {
		t.Fatal(err)
	}

	// An extension that declares no cache has nowhere to mount anything.
	plain := &Extension{ID: "ms-typescript-plain", Name: "typescript", Type: "lsp"}
	plain.Manifest.Capabilities = &Capabilities{LSP: &LSPConfig{FileTypes: []LSPFileType{{Extension: "ts", LanguageID: "typescript"}}}}
	if err := store.Create(plain); err != nil {
		t.Fatal(err)
	}
	rec := putDependencyCache(t, h, plain.ID, t.TempDir())
	if rec.Code != http.StatusBadRequest {
		t.Errorf("extension with no declared cache = %d %s, want 400", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "declares no dependency cache") {
		t.Errorf("error %q does not say why there is nothing to grant", rec.Body)
	}
	// Shape, not just wording: the browser reads the sentence out of {"error": …} and falls back to a
	// generic label for anything else, so a refusal that is merely *readable* here can still be
	// useless in the UI. Same for the path refusals above.
	var shaped struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &shaped); err != nil || shaped.Error == "" {
		t.Errorf("refusal is not a JSON error the browser can read: %s", rec.Body)
	}
	if rec := putDependencyCache(t, h, ext.ID, "/nonexistent/mod-cache"); !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("grant refusal must answer as JSON so the row can show it: %s", rec.Body)
	}

	// Unknown id is a 404, not a grant written to nothing.
	if rec := putDependencyCache(t, h, "no-such-extension", t.TempDir()); rec.Code != http.StatusNotFound {
		t.Errorf("unknown extension = %d, want 404", rec.Code)
	}
}

func TestHandleSetDependencyCache_SamePathTwiceWritesNothing(t *testing.T) {
	// Apply is a button, and a double click must not cost a cold start of a language server
	// someone is in the middle of using.
	h, store := testHandlerWithStore(t)
	ext := lspExtWithCache(t, store)
	dir := t.TempDir()

	if rec := putDependencyCache(t, h, ext.ID, dir); rec.Code != http.StatusOK {
		t.Fatalf("first grant: %d %s", rec.Code, rec.Body)
	}
	before, _ := store.GetByID(ext.ID)
	changed := false
	h.onExtensionChanged = func() { changed = true }

	if rec := putDependencyCache(t, h, ext.ID, dir); rec.Code != http.StatusOK {
		t.Fatalf("second grant: %d %s", rec.Code, rec.Body)
	}
	if changed {
		t.Error("an unchanged grant broadcast a change — every open view would reload for nothing")
	}
	after, _ := store.GetByID(ext.ID)
	if before.SandboxConfig.DependencyHostPath != after.SandboxConfig.DependencyHostPath {
		t.Error("the grant moved on a request that granted the same path")
	}
}

func TestManifestMayNotSupplyTheHostPath(t *testing.T) {
	// The refusal is the security argument: an extension declares where *it* looks and never what
	// it may read on this machine. Stripping the field would let a manifest's value reach the
	// stored record by whichever path forgot to strip it.
	c := &LSPConfig{FileTypes: []LSPFileType{{Extension: "go", LanguageID: "go"}}}
	c.Sandbox = &SandboxConfig{
		Dependency:         &DependencyCache{ContainerPath: "/go/pkg/mod", Env: "GOMODCACHE"},
		DependencyHostPath: "/home/sandbox/go/pkg/mod",
	}
	err := c.Validate()
	if err == nil {
		t.Fatal("a manifest supplying a host path was accepted")
	}
	for _, want := range []string{"dependency_host_path", "Settings -> Extensions", "may not come from a manifest"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	// The declaration alone is fine — this is the shape the bundled manifests ship.
	c.Sandbox.DependencyHostPath = ""
	if err := c.Validate(); err != nil {
		t.Errorf("the declared shape must validate: %v", err)
	}
}

func TestGrantSurvivesAnUpdateFromTheRegistry(t *testing.T) {
	// An update replaces the manifest's declared fields. It must not touch the user's grant: the
	// cache they pointed at is not the extension's to revoke, and losing it silently would put
	// every language server back to answering from an empty image.
	stored := SandboxConfig{
		Dependency:         &DependencyCache{ContainerPath: "/old/mod", Env: "OLD"},
		DependencyHostPath: "/home/user/go/pkg/mod",
	}
	declared := &SandboxConfig{
		Dependency: &DependencyCache{ContainerPath: "/go/pkg/mod", Env: "GOMODCACHE"},
	}
	got := mergeManifestSandboxAdditions(stored, declared)
	if got.DependencyHostPath != "/home/user/go/pkg/mod" {
		t.Errorf("grant after update = %q, want the user's path untouched", got.DependencyHostPath)
	}
	if got.Dependency.Env != "GOMODCACHE" {
		t.Errorf("declaration after update = %+v, want the new manifest's", got.Dependency)
	}

	// And install, which starts from a manifest, must not invent one.
	fromManifest := sandboxConfigFromManifest(&SandboxConfig{
		Dependency:         &DependencyCache{ContainerPath: "/go/pkg/mod", Env: "GOMODCACHE"},
		DependencyHostPath: "/somewhere/typed/into/a/manifest",
	})
	if fromManifest.DependencyHostPath != "" {
		t.Errorf("install carried a host path from the manifest: %q", fromManifest.DependencyHostPath)
	}
}

// TestCheckGrantShapeWithHomes: the grant is read-only for the language server but read-write in every
// sandboxed task, and a task runs code a repository's content helps steer — so one text field must not
// be able to hand over the host's configuration, a whole home directory, or a credential store.
func TestCheckGrantShapeWithHomes(t *testing.T) {
	homes := []string{"/home/dev", "/root"}

	allowed := []string{
		"/home/dev/go/pkg/mod",      // the case the feature exists for
		"/home/dev/.cargo/registry", // a hidden directory of the user's own is not a credential store
		"/home/dev/.cache/mike/mod",
		"/home/other/mike-cache/mod", // somebody else's cache, named by somebody who can read it
		"/tmp/mike-cache/mod",
		"/opt/homebrew/var/govanity", // no system configuration lives under /opt
		"/srv/data/projects/mod",
	}
	for _, path := range allowed {
		if err := checkGrantShapeWithHomes(path, homes); err != nil {
			t.Errorf("%s should be grantable: %v", path, err)
		}
	}

	refused := map[string]string{
		"/etc":                       "close to the root",
		"/tmp":                       "close to the root",
		"/home":                      "close to the root",
		"/etc/ssh":                   "belongs to the machine",
		"/usr/local/go":              "belongs to the machine",
		"/var/cache/gopls/mod":       "belongs to the machine",
		"/proc/self/root/mod":        "belongs to the machine",
		"/home/dev":                  "a home directory itself",
		"/root":                      "a home directory itself",
		"/home/dev/.ssh":             "credentials",
		"/home/dev/.ssh/keys":        "credentials",
		"/home/dev/.config/gh":       "credentials",
		"/root/.aws/credentials-dir": "credentials",
		"/home/dev/.docker":          "credentials",
	}
	for path, why := range refused {
		err := checkGrantShapeWithHomes(path, homes)
		if err == nil {
			t.Errorf("%s was accepted (%s — it should be refused)", path, why)
			continue
		}
		if strings.Contains(err.Error(), "not an absolute path") {
			t.Errorf("%s: wrong rule fired: %v", path, err)
		}
	}
}

// TestValidateDependencyGrant_RefusesWholeDirectories: the same rule through the function the endpoint
// calls, with real directories.
func TestValidateDependencyGrant_RefusesWholeDirectories(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || filepath.Clean(home) == "/" {
		t.Skipf("no usable home directory: %v", err)
	}
	for _, path := range []string{"/", "/etc", "/etc/ssh", "/usr", filepath.Clean(home)} {
		if got, err := ValidateDependencyGrant(path); err == nil {
			t.Errorf("ValidateDependencyGrant(%q) = %q, want refusal", path, got)
		}
	}
}

// putDependencyCacheBody sends an arbitrary body, for the cases where the body itself is the point.
func putDependencyCacheBody(t *testing.T, h *ExtensionHandler, id, body string) (*httptest.ResponseRecorder, error) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/extensions/"+id+"/dependency-cache", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleSetDependencyCache(rec, req, id)
	return rec, nil
}

// TestHandleSetDependencyCache_RequiresTheKey: an empty host_path revokes, so a body that does not
// carry the key at all must be refused — otherwise {"path": ...} takes a working grant away and
// answers 200.
func TestHandleSetDependencyCache_RequiresTheKey(t *testing.T) {
	h, store := testHandlerWithStore(t)
	ext := lspExtWithCache(t, store)
	if _, err := putDependencyCacheBody(t, h, ext.ID, `{"host_path":"`+t.TempDir()+`"}`); err != nil {
		t.Fatal(err)
	}
	extID := ext.ID

	req := httptest.NewRequest(http.MethodPut, "/api/extensions/"+extID+"/dependency-cache", strings.NewReader(`{"path":"/home/dev/go/pkg/mod"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleSetDependencyCache(rec, req, extID)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a body without host_path = %d, want 400: %s", rec.Code, rec.Body.String())
	}

	// And the grant it refused is still there.
	got, err := h.store.GetByID(extID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SandboxConfig.DependencyHostPath == "" {
		t.Error("the request that was refused still revoked the grant")
	}
}
