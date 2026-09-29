package extensions

// The failure these guard is a row that says "installed" with no files behind it: the extension stays
// enabled in the UI, its served document 404s, and it silently does nothing. The app restores those
// files at startup from the commit each row pins — so what matters is which rows are trusted with a
// recursive write, where that write may land, and that the bytes restored are the pinned ones.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mike/pkg/sandboxruntime"
)

// handlerFixture is a handler whose extensions directory is an empty temp dir, plus that directory.
func handlerFixture(t *testing.T) (*ExtensionHandler, *ExtensionStore, string) {
	t.Helper()
	extStore := testStore(t)
	reg := NewRegistryClient()
	dir := t.TempDir()
	cfg := &ExtensionConfig{ExtensionsDir: dir}
	return NewExtensionHandler(extStore, NewExtensionSandboxManager(reg, extStore, sandboxruntime.Boot{}), reg, cfg), extStore, dir
}

// createRow stores an installed extension. The author is kept in the shape install stores it: the
// already-slugified directory name.
func createRow(t *testing.T, store *ExtensionStore, id, author, name, source, repo, commit, subdir string) *Extension {
	t.Helper()
	ext := &Extension{
		ID: id, Name: name, Author: author, Version: "1.0.0", Type: "ui",
		Enabled: true, InstallSource: source, Repo: repo, Commit: commit, Subdir: subdir,
	}
	ext.Manifest = ExtensionManifest{DisplayName: name, Version: "1.0.0", Author: author}
	if err := store.Create(ext); err != nil {
		t.Fatalf("create extension %s: %v", id, err)
	}
	return ext
}

// writeManifest puts a manifest.json in an extension's directory, as a completed install would have.
func writeManifest(t *testing.T, root, author, name, body string) string {
	t.Helper()
	dir := filepath.Join(root, author, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func manifestBody(author, version string) string {
	b, _ := json.Marshal(ExtensionManifest{DisplayName: "X", Author: author, Version: version})
	return string(b)
}

func classifyByID(t *testing.T, h *ExtensionHandler) map[string]missingInstall {
	t.Helper()
	missing, err := h.classifyMissingInstalls()
	if err != nil {
		t.Fatalf("classifyMissingInstalls: %v", err)
	}
	out := map[string]missingInstall{}
	for _, m := range missing {
		out[m.ext.ID] = m
	}
	return out
}

// A row is reported when its directory has no manifest it can be loaded from — and only then. A
// half-written checkout has a directory; that is not an install.
func TestClassifyMissingInstalls(t *testing.T) {
	t.Parallel()
	h, store, root := handlerFixture(t)

	writeManifest(t, root, "acme", "healthy", manifestBody("Acme", "1.0.0"))
	createRow(t, store, "acme-healthy-1", "acme", "healthy", "registry", "https://example.invalid/r", strings.Repeat("a", 64), "ui/healthy")

	// Gone entirely.
	createRow(t, store, "acme-gone-2", "acme", "gone", "registry", "https://example.invalid/r", strings.Repeat("b", 64), "ui/gone")
	// A directory with no manifest: a checkout that died halfway.
	if err := os.MkdirAll(filepath.Join(root, "acme", "half"), 0o755); err != nil {
		t.Fatal(err)
	}
	createRow(t, store, "acme-half-3", "acme", "half", "registry", "https://example.invalid/r", strings.Repeat("c", 64), "ui/half")
	// A manifest that does not parse is as unusable as none.
	writeManifest(t, root, "acme", "corrupt", "{not json")
	createRow(t, store, "acme-corrupt-4", "acme", "corrupt", "registry", "https://example.invalid/r", strings.Repeat("d", 64), "ui/corrupt")
	// A local install's files WERE the install: reported, never cloned.
	createRow(t, store, "local-plain-5", "local", "plain", "local", "", "", "")
	// A git source that could not be fetched is reported with its reason, not attempted.
	createRow(t, store, "acme-badcommit-6", "acme", "badcommit", "registry", "https://example.invalid/r", "main", "ui/badcommit")
	// A name that is not a directory name never reaches a filesystem operation.
	createRow(t, store, "acme-escape-7", "acme", "../escape", "registry", "https://example.invalid/r", strings.Repeat("e", 64), "ui/escape")

	got := classifyByID(t, h)

	for _, tc := range []struct {
		id         string
		repairable bool
		wantWhy    string
	}{
		{"acme-gone-2", true, "files are not installed"},
		{"acme-half-3", true, "files are not installed"},
		{"acme-corrupt-4", true, "not valid JSON"},
		{"local-plain-5", false, "nothing to restore it from"},
		{"acme-badcommit-6", false, "git source on the record is unusable"},
		{"acme-escape-7", false, "not a directory name"},
	} {
		m, ok := got[tc.id]
		if !ok {
			t.Errorf("%s: not reported missing", tc.id)
			continue
		}
		if m.repairable != tc.repairable {
			t.Errorf("%s: repairable = %v, want %v (%s)", tc.id, m.repairable, tc.repairable, m.why)
		}
		if !strings.Contains(m.why, tc.wantWhy) {
			t.Errorf("%s: why = %q, want it to mention %q", tc.id, m.why, tc.wantWhy)
		}
	}
	if _, ok := got["acme-healthy-1"]; ok {
		t.Error("an install with a readable manifest must not be reported")
	}
	if len(got) != 6 {
		t.Errorf("reported %d rows, want 6: %v", len(got), got)
	}
}

// commitRepo writes files into an existing fixture repo and returns the new commit.
func commitRepo(t *testing.T, repo string, files map[string]string) string {
	t.Helper()
	for path, content := range files {
		full := filepath.Join(repo, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
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
	run("add", "-A")
	run("commit", "-qm", "next")
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// The restore is the pinned commit, not whatever the repository says now: a repair that pulled the
// newest revision would be an unasked-for update with unvetted bytes.
func TestRepairMissingFiles_RestoresThePinnedCommit(t *testing.T) {
	t.Parallel()
	repo, pinned := makeGitRepo(t, map[string]string{
		"ui/x/manifest.json": manifestBody("Acme", "1.0.0"),
		"ui/x/Preview.js":    "PINNED CONTENT",
	})
	newest := commitRepo(t, repo, map[string]string{"ui/x/Preview.js": "NEWER CONTENT"})
	if newest == pinned {
		t.Fatal("fixture did not produce a second commit")
	}

	h, store, root := handlerFixture(t)
	createRow(t, store, "acme-x-1", "acme", "x", "registry", repo, pinned, "ui/x")

	changed := 0
	h.SetOnExtensionChanged(func() { changed++ })

	repaired, refused, err := h.RepairMissingFiles(context.Background())
	if err != nil {
		t.Fatalf("RepairMissingFiles: %v", err)
	}
	if len(refused) != 0 || len(repaired) != 1 || repaired[0] != "acme-x-1" {
		t.Fatalf("repaired=%v refused=%v, want exactly acme-x-1 restored", repaired, refused)
	}

	got, err := os.ReadFile(filepath.Join(root, "acme", "x", "Preview.js"))
	if err != nil {
		t.Fatalf("restored file missing: %v", err)
	}
	if string(got) != "PINNED CONTENT" {
		t.Errorf("restored %q, want the pinned commit's bytes", got)
	}
	if _, err := os.Stat(filepath.Join(root, "acme", "x", "manifest.json")); err != nil {
		t.Errorf("manifest.json not restored: %v", err)
	}
	// The files reappearing changes what the app can offer, once per pass, not once per row.
	if changed != 1 {
		t.Errorf("onExtensionChanged called %d times, want 1", changed)
	}

	// A second pass has nothing to do and says so by doing nothing.
	if _, _, err := h.RepairMissingFiles(context.Background()); err != nil {
		t.Fatalf("second RepairMissingFiles: %v", err)
	}
	if changed != 1 {
		t.Errorf("a quiet pass must not report another change, changed=%d", changed)
	}
}

// A repair must not overwrite work in progress: the same in-flight guard an install or update takes.
func TestRepairMissingFiles_SkipsInFlightExtension(t *testing.T) {
	t.Parallel()
	repo, commit := makeGitRepo(t, map[string]string{"ui/x/manifest.json": manifestBody("Acme", "1.0.0")})
	h, store, root := handlerFixture(t)
	createRow(t, store, "acme-x-1", "acme", "x", "registry", repo, commit, "ui/x")

	if !h.inFlightOps.begin("x") {
		t.Fatal("fixture could not take the in-flight guard")
	}

	repaired, refused, err := h.RepairMissingFiles(context.Background())
	if err != nil {
		t.Fatalf("RepairMissingFiles: %v", err)
	}
	if len(repaired) != 0 {
		t.Errorf("repaired %v while an operation on that name is running", repaired)
	}
	// A skip is not a failure: the operation holding the guard may be installing those very files.
	if len(refused) != 0 {
		t.Errorf("refused = %v, want a skip to be neither a repair nor a failure", refused)
	}
	if _, err := os.Stat(filepath.Join(root, "acme", "x")); !os.IsNotExist(err) {
		t.Error("the skipped extension's files were written anyway")
	}
	h.inFlightOps.end("x")
}

// A row with no git source is reported and left alone; a repo the transport fence refuses must never
// reach a subprocess at all — that is the same clone path an install uses, run here without anyone
// asking for it.
func TestRepairMissingFiles_NeverFetchesWhatItCannotTrust(t *testing.T) {
	t.Parallel()
	sentinel := filepath.Join(t.TempDir(), "pwned")
	repo, commit := makeGitRepo(t, map[string]string{"ui/x/manifest.json": manifestBody("Acme", "1.0.0")})

	h, store, root := handlerFixture(t)
	// No source at all.
	createRow(t, store, "local-a-1", "local", "a", "local", "", "", "")
	// An ext:: URL would run a host command if it reached git.
	createRow(t, store, "acme-b-2", "acme", "b", "registry", "ext::sh -c 'touch "+sentinel+"'", commit, "ui/b")
	// A good source, so the pass has something to restore and the above are not skipped for its benefit.
	createRow(t, store, "acme-c-3", "acme", "c", "registry", repo, commit, "ui/x")

	repaired, refused, err := h.RepairMissingFiles(context.Background())
	if err != nil {
		t.Fatalf("RepairMissingFiles: %v", err)
	}
	if len(repaired) != 1 || repaired[0] != "acme-c-3" {
		t.Errorf("repaired = %v, want only acme-c-3", repaired)
	}
	if len(refused) != 0 {
		t.Errorf("rows that were reported as unrepairable should not count as failures: %v", refused)
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Fatal("an ext:: repo was handed to git — the transport fence did not run")
	}
	// Nothing was invented for the local row.
	if _, err := os.Stat(filepath.Join(root, "a")); !os.IsNotExist(err) {
		t.Error("a local install was materialized from nowhere")
	}
}

// The pinned commit's subdir may move or stop being an extension. Restoring it under a name it was
// never vetted under is the failure this refuses.
func TestRepairMissingFiles_RefusesWhatIsNotTheRecordedExtension(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
	}{
		{"no manifest at all", "{}"},
		{"another author", manifestBody("SomeoneElse", "1.0.0")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			files := map[string]string{"ui/x/other.txt": "x"}
			if tc.content != "{}" {
				files["ui/x/manifest.json"] = tc.content
			}
			repo, commit := makeGitRepo(t, files)
			h, store, _ := handlerFixture(t)
			createRow(t, store, "acme-x-1", "acme", "x", "registry", repo, commit, "ui/x")

			changed := 0
			h.SetOnExtensionChanged(func() { changed++ })
			repaired, refused, err := h.RepairMissingFiles(context.Background())
			if err != nil {
				t.Fatalf("RepairMissingFiles: %v", err)
			}
			if len(repaired) != 0 {
				t.Errorf("repaired = %v, want nothing called repaired", repaired)
			}
			if len(refused) != 1 {
				t.Errorf("refused = %v, want the row reported", refused)
			}
			if changed != 0 {
				t.Error("a refused restore must not tell the app the extension set changed")
			}
		})
	}
}

// A cancelled context ends the pass instead of starting fetches nobody wants any more.
func TestRepairMissingFiles_EndsWithTheContext(t *testing.T) {
	t.Parallel()
	repo, commit := makeGitRepo(t, map[string]string{"ui/x/manifest.json": manifestBody("Acme", "1.0.0")})
	h, store, root := handlerFixture(t)
	createRow(t, store, "acme-x-1", "acme", "x", "registry", repo, commit, "ui/x")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	repaired, refused, err := h.RepairMissingFiles(ctx)
	if err != nil {
		t.Fatalf("RepairMissingFiles: %v", err)
	}
	if len(repaired) != 0 || len(refused) != 1 {
		t.Errorf("repaired=%v refused=%v, want nothing restored and the row reported", repaired, refused)
	}
	if _, err := os.Stat(filepath.Join(root, "acme", "x")); !os.IsNotExist(err) {
		t.Error("a cancelled pass still wrote files")
	}
}
