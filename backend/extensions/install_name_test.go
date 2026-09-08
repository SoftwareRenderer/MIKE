package extensions

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// sourceRepo makes a one-commit git repo to act as the registry's pinned source and returns
// its commit hash. Identity goes on the command line, not from the machine's git config.
func sourceRepo(t *testing.T) (dir, commit string) {
	t.Helper()
	dir = t.TempDir()
	manifest := `{"name":"good","version":"1.0.0","author":"someone","type":"ui"}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		full := append([]string{"-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "init.defaultBranch=main"}, args...)
		out, err := exec.Command("git", full...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", dir)
	git("-C", dir, "add", ".")
	git("-C", dir, "commit", "-q", "-m", "extension")
	return dir, git("-C", dir, "rev-parse", "HEAD")
}

// installEntry runs one install the way HandleInstall does after the index was fetched and
// returns the failure the SSE stream reported ("" when it succeeded).
func installEntry(t *testing.T, h *ExtensionHandler, entry *RegistryEntry) string {
	t.Helper()
	events := make(chan sseEvent, 32)
	failure := ""
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range events {
			if ev.Error != "" {
				failure = ev.Error
			}
		}
	}()
	h.installInBackground(entry.Name, entry, "", events)
	// close(done) is the write's happens-before edge for reading `failure`.
	<-done
	return failure
}

// TestInstallRefusesRegistryNameOutsideExtensionsDir (security): a registry `name` derives
// the directory the checkout is RemoveAll'd into, so a separator reaches outside it.
func TestInstallRefusesRegistryNameOutsideExtensionsDir(t *testing.T) {
	repo, commit := sourceRepo(t)
	root := t.TempDir()
	extRoot := filepath.Join(root, "extensions")
	if err := os.MkdirAll(extRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(root, "victim")
	victimFile := filepath.Join(victim, "important.txt")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(victimFile, []byte("do not delete me"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &ExtensionConfig{ExtensionsDir: extRoot}
	h := NewExtensionHandler(testStore(t), NewExtensionSandboxManager(NewRegistryClient(), testStore(t)), NewRegistryClient(), cfg)

	entry := &RegistryEntry{
		// One ".." only reaches a sibling inside the extensions root, which would prove nothing.
		Name:   filepath.Join("..", "..", "victim"),
		Author: "someone",
		Type:   ExtensionTypeUI,
		Repo:   "file://" + repo,
		Commit: commit,
	}
	if failure := installEntry(t, h, entry); !strings.Contains(failure, "not a directory name") {
		t.Fatalf("install of an escaping name reported %q", failure)
	}

	if _, err := os.Stat(victimFile); err != nil {
		t.Fatalf("the install removed a directory outside the extensions root: %v", err)
	}
	if entries, err := os.ReadDir(victim); err != nil || len(entries) != 1 {
		t.Fatalf("the install wrote into %s: %v (%v)", victim, entries, err)
	}
}

// TestInstallRefusesLocalDockerfileURL (security): dockerfile_url is built by podman build,
// which runs RUN steps on the host runtime, so a local URL there is a host file read.
func TestInstallRefusesLocalDockerfileURL(t *testing.T) {
	file := filepath.Join(t.TempDir(), "Dockerfile")
	if err := os.WriteFile(file, []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		url  string
		ok   bool
	}{
		{"absolute local file", "file://" + file, false},
		// Relative to the registry; the two below are absolute forms a file:// base would open.
		{"absolute path", "/etc/passwd", false},
		{"registry-relative", "good/Dockerfile", true},
		{"https", "https://registry.example/good/Dockerfile", true},
		{"http", "http://registry.example/good/Dockerfile", true},
	} {
		err := validateRemoteAssetURL("dockerfile_url", tc.url)
		if tc.ok && err != nil {
			t.Errorf("%s: refused a legal URL: %v", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: accepted %q", tc.name, tc.url)
		}
	}

	// The scheme rule must reach the entry, not only the helper.
	if err := refuseUninstallable(&RegistryEntry{Name: "good", DockerfileURL: "file:///etc/shadow"}); err == nil {
		t.Error("an entry naming a local Dockerfile was accepted")
	}
}

// TestInstallAcceptsOrdinaryEntry is the control: the guard is a rule, not a broken install.
func TestInstallAcceptsOrdinaryEntry(t *testing.T) {
	repo, commit := sourceRepo(t)
	root := t.TempDir()
	extRoot := filepath.Join(root, "extensions")
	cfg := &ExtensionConfig{ExtensionsDir: extRoot}
	h := NewExtensionHandler(testStore(t), NewExtensionSandboxManager(NewRegistryClient(), testStore(t)), NewRegistryClient(), cfg)

	entry := &RegistryEntry{Name: "good", Author: "someone", Type: ExtensionTypeUI, Repo: "file://" + repo, Commit: commit}
	if err := refuseUninstallable(entry); err != nil {
		t.Fatalf("an ordinary entry was refused: %v", err)
	}
	installEntry(t, h, entry)
	if _, err := os.Stat(filepath.Join(extRoot, "someone", "good", "manifest.json")); err != nil {
		t.Fatalf("the ordinary install did not materialize its files: %v", err)
	}
	if rows, err := h.store.List(); err != nil || len(rows) != 1 {
		t.Fatalf("installed rows = %v (%v)", rows, err)
	}
}
