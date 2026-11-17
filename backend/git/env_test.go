package git

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// writeHook installs an executable hook in a repo's .git/hooks and returns the marker path it
// touches. The hook's body is the thing under test's behalf: if it ran, the marker exists.
func writeHook(t *testing.T, repo, hook, body string) string {
	t.Helper()
	hooksDir := filepath.Join(repo, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	marker := filepath.Join(t.TempDir(), hook+"-ran")
	path := filepath.Join(hooksDir, hook)
	script := fmt.Sprintf("#!/bin/sh\ntouch %q\n%s\n", marker, body)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}
	return marker
}

// initRepo creates a throwaway repo with one commit, hermetically: the identity is passed with -c
// so the test does not depend on the machine's git config (AGENTS.md: some tests here shell out to
// a real git commit and need an identity; this one refuses to inherit one).
func initRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := Command(repo, args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", ".")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	run("add", "a.txt")
	run("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "base")
	return repo
}

// TestPreCommitHookNeverRuns: a repository's own pre-commit hook runs arbitrary code as the server
// user. The hook exits 1, so "the commit succeeded" is an independent assertion that the hook did not
// run, not just a missing marker file.
func TestPreCommitHookNeverRuns(t *testing.T) {
	repo := initRepo(t)
	marker := writeHook(t, repo, "pre-commit", "exit 1")

	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("modify file: %v", err)
	}
	cmd := Command(repo, "add", "a.txt")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	cmd = Command(repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid",
		"commit", "-m", "fenced commit")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit failed — the pre-commit hook is NOT fenced: %v\n%s", err, out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("pre-commit hook ran (marker exists) despite the execution fence")
	}
}

// TestExecutionFenceIsWhatStopsTheHook is the control case. Without it the test above passes just as
// well if the fence ever silently stops being applied, or if git changes where it looks for hooks:
// nothing would have proved the hook was capable of firing in this environment.
func TestExecutionFenceIsWhatStopsTheHook(t *testing.T) {
	repo := initRepo(t)
	marker := writeHook(t, repo, "pre-commit", "exit 1")

	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("modify file: %v", err)
	}
	// Deliberately an unfenced process: os.Environ(), no core.hooksPath. This is the shape the
	// codebase had before backend/git/env.go existed.
	cmd := exec.Command(gitBinary, "-C", repo, "add", "a.txt")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	cmd = exec.Command(gitBinary, "-C", repo,
		"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "unfenced")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("control case: unfenced commit SUCCEEDED — the hook cannot fire here, so every " +
			"hook test in this file is vacuous and must be rewritten")
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("control case: unfenced commit failed (%v) but the hook left no marker, so the "+
			"failure was not the hook: %s", err, out)
	}
}

// setRepoConfig writes one value into the repository's own .git/config, which is the shape a repo
// arrives with when it was copied, restored or unpacked rather than cloned (`git clone` writes a
// fresh config and does not carry [core] over).
func setRepoConfig(t *testing.T, repo, key, value string) {
	t.Helper()
	cmd := Command(repo, "config", "--local", key, value)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config %s: %v\n%s", key, err, out)
	}
}

// fsmonitorMarker is the command a hostile core.fsmonitor would run: append to a file outside the
// repo, so "the marker exists" is the whole assertion.
func fsmonitorMarker(t *testing.T, repo string) string {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "fsmonitor-ran")
	setRepoConfig(t, repo, "core.fsmonitor", "echo FIRED >> "+marker)
	return marker
}

// TestRepoFsmonitorNeverRuns covers the config-driven half of the fence. core.fsmonitor is not a
// hook, so core.hooksPath never reached it: `git status` — which this server runs continuously in
// every registered checkout — executes the command the repository named.
func TestRepoFsmonitorNeverRuns(t *testing.T) {
	repo := initRepo(t)
	marker := fsmonitorMarker(t, repo)

	if out, err := Command(repo, "status", "--porcelain").CombinedOutput(); err != nil {
		t.Fatalf("git status with the fence: %v\n%s", err, out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("core.fsmonitor from the repository's own config ran despite the fence")
	}
}

// TestUnfencedFsmonitorRuns is the control case: the config value must be capable of firing in this
// environment, or the test above proves nothing about the fence.
func TestUnfencedFsmonitorRuns(t *testing.T) {
	repo := initRepo(t)
	marker := fsmonitorMarker(t, repo)

	// Deliberately an unfenced process: os.Environ(), no GIT_CONFIG_* — the shape this repo had
	// before backend/git/env.go existed.
	cmd := exec.Command(gitBinary, "-C", repo, "status", "--porcelain")
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("unfenced git status: %v\n%s", err, out)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("control case: an unfenced git status did NOT run core.fsmonitor, so every "+
			"fsmonitor assertion in this file is vacuous and must be rewritten (git said: %s)", out)
	}
}

// TestPostCheckoutHookNeverRunsOnWorktreeAdd covers the case --no-verify structurally cannot:
// `git worktree add` has no --no-verify flag, and it runs post-checkout. CreateWorktree is a hot
// path, so this is the hook a repository would most reliably get executed.
func TestPostCheckoutHookNeverRunsOnWorktreeAdd(t *testing.T) {
	repo := initRepo(t)
	marker := writeHook(t, repo, "post-checkout", "exit 0")

	addWorktree := func(t *testing.T, fenced bool) bool {
		t.Helper()
		wt := filepath.Join(t.TempDir(), "wt")
		var cmd *exec.Cmd
		if fenced {
			cmd = Command(repo, "worktree", "add", "-q", "--detach", wt, "HEAD")
		} else {
			cmd = exec.Command(gitBinary, "-C", repo, "worktree", "add", "-q", "--detach", wt, "HEAD")
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("worktree add (fenced=%v): %v\n%s", fenced, err, out)
		}
		_, err := os.Stat(marker)
		return err == nil
	}

	if ran := addWorktree(t, true); ran {
		t.Fatal("post-checkout ran on `git worktree add` despite the execution fence")
	}
	if ran := addWorktree(t, false); !ran {
		t.Fatal("control case: post-checkout did not run unfenced — the assertion above is vacuous")
	}
}

// One rule, three entry points: nothing the server was launched with reaches a git subprocess, and
// every one of them was the leak once. Env() is the allowlist itself, CloneEnv() was
// `append(os.Environ(), …)` for years — and a clone is the one git operation whose arguments come
// straight from an HTTP request — and Command() is the only place this repo builds a git process, so
// an empty cmd.Env there would hand os/exec the whole server environment. Each entry point is checked
// against the same secret set and for the policy it exists to apply, so a future "simplification"
// back to os.Environ() cannot keep the assertion by accident.
func TestNoServerEnvironmentReachesAGitSubprocess(t *testing.T) {
	secrets := map[string]string{
		"MIKE_ENCRYPTION_KEY":          "the-key-that-decrypts-every-provider",
		"DANGER_ZONE":                  "1",
		"MIKE_LISTEN":                  "0.0.0.0",
		"MIKE_ALLOWED_HOSTS":           "evil.example",
		"MIKE_REGISTRY_URL":            "https://attacker.example/registry.json",
		"MIKE_LOGIN_RATE_LIMIT":        "999",
		"MIKE_EXT_MOUNT_ROOTS":         "/etc",
		"MIKE_TRUST_PROXY":             "1",
		"AWS_SECRET_ACCESS_KEY":        "shhh",
		"GITHUB_TOKEN":                 "ghp_shhh",
		"SOME_NEW_SECRET_THE_OPS_TEAM": "added tomorrow",
	}
	for k, v := range secrets {
		t.Setenv(k, v)
	}
	dir := t.TempDir()

	for _, entry := range []struct {
		name string
		env  func(t *testing.T) []string
		want []string
	}{
		{"Env", func(*testing.T) []string { return Env() }, []string{"core.hooksPath", "GIT_TERMINAL_PROMPT=0"}},
		{
			"CloneEnv",
			func(*testing.T) []string { return CloneEnv() },
			[]string{"core.hooksPath", "GIT_ALLOW_PROTOCOL=", "GIT_TERMINAL_PROMPT=0"},
		},
		{
			"Command",
			func(t *testing.T) []string {
				cmd := Command(dir, "rev-parse", "--git-dir")
				if cmd.Dir != dir {
					t.Errorf("Command(dir).Dir = %q, want %q", cmd.Dir, dir)
				}
				return cmd.Env
			},
			[]string{"core.hooksPath", "GIT_TERMINAL_PROMPT=0"},
		},
		{
			// The identity-carrying entry point rebuilds cmd.Env, so it is the one place a later edit
			// could hand os/exec os.Environ() while still passing the identity assertions.
			"CommandWithIdentity",
			func(*testing.T) []string {
				return CommandWithIdentity("", ProjectIdentity("Ada Lovelace", "ada@example.com"), "commit", "-m", "x").Env
			},
			[]string{"core.hooksPath", "GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=Ada Lovelace"},
		},
	} {
		env := entry.env(t)
		if len(env) == 0 {
			t.Fatalf("%s handed os/exec an empty environment: the subprocess inherits the whole server environment", entry.name)
		}
		for _, kv := range env {
			name, _, _ := strings.Cut(kv, "=")
			if value, isSecret := secrets[name]; isSecret {
				t.Errorf("%s leaked %s=%q to a git subprocess", entry.name, name, value)
			}
			if strings.HasPrefix(name, "MIKE_") {
				t.Errorf("%s leaked MIKE_* configuration variable %s", entry.name, name)
			}
		}
		joined := strings.Join(env, "\n")
		for _, want := range entry.want {
			if !strings.Contains(joined, want) {
				t.Errorf("%s lost %q — the policy that entry point exists to apply", entry.name, want)
			}
		}
	}
}

// Env()'s allowlist has three failure shapes, all of them about which names the child sees: git's own
// credentials going missing (pushing to a private remote is a supported workflow, and this is why
// backend/agent/env.go once sanctioned os.Environ() for this package), an allowed name present but
// empty (ssh-agent clients read `SSH_AUTH_SOCK=` as a configured-but-broken agent instead of falling
// back), and a caller's extra outranking the policy (os/exec keeps the LAST occurrence, so policy
// after extras is what makes the execution fence unforgeable from a call site).
func TestEnvCarriesWhatGitNeedsAndNothingElse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setenv map[string]string
		extras []string
		want   []string
		// last names a GIT_CONFIG key whose LAST occurrence must carry that value.
		last map[string]string
	}{
		{
			name: "git's own credentials survive",
			setenv: map[string]string{
				"HOME":            "/home/operator",
				"USER":            "operator",
				"PATH":            "/usr/bin",
				"SSH_AUTH_SOCK":   "/run/user/1000/vscode-ssh-auth-sock",
				"XDG_RUNTIME_DIR": "/run/user/1000",
				"LANG":            "en_US.UTF-8",
				"LC_ALL":          "C.UTF-8",
				"HTTPS_PROXY":     "http://proxy.internal:3128",
				"SSL_CERT_FILE":   "/etc/ssl/certs/internal-ca.pem",
			},
			want: []string{
				"HOME=/home/operator", "USER=operator", "PATH=/usr/bin",
				"SSH_AUTH_SOCK=/run/user/1000/vscode-ssh-auth-sock",
				"XDG_RUNTIME_DIR=/run/user/1000", "LANG=en_US.UTF-8", "LC_ALL=C.UTF-8",
				"HTTPS_PROXY=http://proxy.internal:3128", "SSL_CERT_FILE=/etc/ssl/certs/internal-ca.pem",
			},
		},
		{
			name:   "an allowed name the server lacks is omitted, not emitted empty",
			setenv: map[string]string{"HOME": "/home/operator", "SSH_AUTH_SOCK": ""},
			want:   []string{"HOME=/home/operator"},
		},
		{
			name:   "the policy survives a caller's extra",
			extras: []string{"GIT_CONFIG_KEY_0=core.editor", "core.hooksPath=/tmp/attacker"},
			want:   []string{"GIT_TERMINAL_PROMPT=0"},
			last:   map[string]string{"GIT_CONFIG_VALUE_0": "/dev/null"},
		},
	} {
		for k, v := range tc.setenv {
			t.Setenv(k, v)
		}
		env := Env(tc.extras...)

		for _, want := range tc.want {
			if !slices.Contains(env, want) {
				t.Errorf("%s: %q missing from Env() — %v", tc.name, want, env)
			}
		}
		for name, value := range tc.setenv {
			if value == "" && slices.Contains(env, name+"=") {
				t.Errorf("%s: Env() emitted an empty %s= instead of omitting it", tc.name, name)
			}
		}
		for key, want := range tc.last {
			got, seen := "", false
			for _, kv := range env {
				if v, ok := strings.CutPrefix(kv, key+"="); ok {
					got, seen = v, true
				}
			}
			if !seen {
				t.Errorf("%s: %s absent from Env() entirely — the execution fence is not being applied", tc.name, key)
			} else if got != want {
				t.Errorf("%s: execution fence overridable, last %s = %q, want %q", tc.name, key, got, want)
			}
		}
	}
}

func TestExecutionFenceShape(t *testing.T) {
	fence := ExecutionFence()
	got := strings.Join(fence, "\n")

	// Every program git would run on a repository's say-so is pinned here. A new key must be
	// added to the count too, or git ignores the whole set in silence — which would read as
	// "the fence is fine".
	for _, want := range []string{
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0=/dev/null",
		"GIT_CONFIG_KEY_1=core.fsmonitor",
		"GIT_CONFIG_VALUE_1=false",
		"GIT_CONFIG_KEY_2=core.editor",
		"GIT_CONFIG_VALUE_2=true",
		"GIT_CONFIG_KEY_3=core.pager",
		"GIT_CONFIG_VALUE_3=cat",
		"GIT_CONFIG_KEY_4=core.askPass",
		"GIT_CONFIG_VALUE_4=",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ExecutionFence() missing %q; full value:\n%s", want, got)
		}
	}

	// The count is the one thing about this mechanism that fails silently, so it is derived here
	// rather than repeated.
	keys := 0
	for _, entry := range fence {
		if strings.HasPrefix(entry, "GIT_CONFIG_KEY_") {
			keys++
		}
	}
	if want := fmt.Sprintf("GIT_CONFIG_COUNT=%d", keys); !strings.Contains(got, want) {
		t.Errorf("ExecutionFence() declares %d KEY_n entries but not %q; git would ignore every one:\n%s", keys, want, got)
	}
}

// editorMarker points the repository's own editor at a file outside the repo, so "the marker
// exists" is the whole assertion.
func editorMarker(t *testing.T, repo string) string {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "editor-ran")
	setRepoConfig(t, repo, "core.editor", "touch "+marker)
	return marker
}

// stageOneChange leaves one staged change, so a commit with no message must reach for an editor.
func stageOneChange(t *testing.T, repo string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("modify file: %v", err)
	}
	if out, err := Command(repo, "add", "a.txt").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
}

// TestRepoEditorNeverRuns covers the program git runs when a commit arrives with no message. Conflict
// resolution ends in `git merge --continue`, which takes its message from an editor, so a repository
// that supplies `core.editor` would have that program run by the server.
func TestRepoEditorNeverRuns(t *testing.T) {
	repo := initRepo(t)
	marker := editorMarker(t, repo)
	stageOneChange(t, repo)

	// The commit is expected to fail: GIT_EDITOR accepts an empty message, which git refuses.
	out, _ := Command(repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit").CombinedOutput()
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("core.editor from the repository's own config ran despite the fence; git said: %s", out)
	}
}

// TestUnfencedRepoEditorRuns is the control case: without GIT_EDITOR the repository's editor must
// really run here, or the fenced assertion above proves nothing.
func TestUnfencedRepoEditorRuns(t *testing.T) {
	repo := initRepo(t)
	marker := editorMarker(t, repo)
	stageOneChange(t, repo)

	cmd := exec.Command(gitBinary, "-C", repo,
		"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit")
	cmd.Env = os.Environ()
	out, _ := cmd.CombinedOutput()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("control case: an unfenced commit did NOT run the repository's core.editor, so the "+
			"editor assertions here are vacuous (git said: %s)", out)
	}
}

// TestMergeContinueSurvivesTheEditorFence is why the editor is pinned to `true` and not to something
// that refuses: conflict resolution ends in `git merge --continue`, which commits the prepared merge
// message through an editor.
func TestMergeContinueSurvivesTheEditorFence(t *testing.T) {
	repo := initRepo(t)
	run := func(expectFail bool, args ...string) string {
		t.Helper()
		identity := []string{"-c", "user.name=Test", "-c", "user.email=test@example.invalid"}
		out, err := Command(repo, append(append([]string{}, identity...), args...)...).CombinedOutput()
		if expectFail && err == nil {
			t.Fatalf("expected %v to report conflicts: %s", args, out)
		}
		if !expectFail && err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}

	base := strings.TrimSpace(run(false, "symbolic-ref", "--short", "HEAD"))
	run(false, "checkout", "-qb", "side")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("from side\n"), 0o644); err != nil {
		t.Fatalf("side edit: %v", err)
	}
	run(false, "commit", "-qam", "side")
	run(false, "checkout", "-q", base)
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("from base\n"), 0o644); err != nil {
		t.Fatalf("base edit: %v", err)
	}
	run(false, "commit", "-qam", "base moved")
	run(false, "checkout", "-q", "side")
	run(true, "merge", "--no-edit", base)

	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("resolved\n"), 0o644); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	run(false, "add", "a.txt")
	run(false, "merge", "--continue")

	// A merge commit has two parents; that it exists is the proof the flow completed.
	if out, err := Command(repo, "rev-parse", "--verify", "HEAD^2").CombinedOutput(); err != nil {
		t.Fatalf("merge --continue left no merge commit: %v\n%s", err, out)
	}
}

// moduleRoot walks up from the test's working directory to the module root, without shelling out
// to `go` (a test that requires its own toolchain cannot run in a trimmed test environment).
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}

// notOurTree lists the paths, relative to the module root, that are not this repo's code:
// extensions/ holds pulled artifacts from another module (the pi adapter, tested over in
// ../MIKE-extensions/agent/pi), and .worktrees/ holds full checkouts of this same tree.
//
// These are matched on the path RELATIVE TO THE ROOT, never on the base name. The first version
// skipped any directory named "extensions", which also skipped backend/extensions/ — hiding three
// git spawns in backend/extensions/registry.go behind the name of an unrelated top-level directory.
var notOurTree = []string{".worktrees", "node_modules", "extensions", "vendor", "dist"}

func skipPath(rel string) bool {
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return false
	}
	first, _, _ := strings.Cut(rel, "/")
	for _, s := range notOurTree {
		if first == s {
			return true
		}
	}
	// Dot-directories (.git, and anything editor- or tool-created) are never our Go source.
	return strings.HasPrefix(first, ".")
}

// TestNoGitSubprocessSpawnsOutsideGit is the structural gate. The environment allowlist and the
// execution fence are only real if there is one place that starts git: every additional
// exec.Command("git") is a spawn point that would have to remember both, and there were 71 of them
// across 12 files before env.go existed. A source-walking test is the established shape for this in
// this repo (frontend/i18n-gate.py, frontend/html-sinks.py) and, unlike a comment in AGENTS.md, it
// fails the build.
func TestNoGitSubprocessSpawnsOutsideGit(t *testing.T) {
	root := moduleRoot(t)
	const spawnFile = "backend/git/env.go" // compared against filepath.ToSlash(rel)

	var offenders []string
	for _, sub := range []string{"backend", "pkg", "scripts"} {
		err := filepath.Walk(filepath.Join(root, sub), func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				relDir, _ := filepath.Rel(root, path)
				if skipPath(relDir) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			// _test.go is exempt for a reason beyond churn: the invariant protects the server
			// process, and a test needs to construct its own fixtures — including, in this very file,
			// an intentionally UNFENCED git subprocess as the control case that proves the fence is
			// what stops a hook. Fencing tests would make the control unwriteable.
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			rel, _ := filepath.Rel(root, path)
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for lineNo := 0; sc.Scan(); lineNo++ {
				line := sc.Text()
				if !isGitSpawn(line) {
					continue
				}
				if filepath.ToSlash(rel) == spawnFile {
					continue
				}
				offenders = append(offenders, fmt.Sprintf("%s:%d: %s", rel, lineNo+1, strings.TrimSpace(line)))
			}
			return sc.Err()
		})
		if err != nil {
			t.Fatalf("walk %s: %v", sub, err)
		}
	}

	if len(offenders) > 0 {
		t.Errorf("%d git subprocess(es) constructed outside %s. Use git.Command(dir, args...) so "+
			"the environment allowlist and the execution fence cannot be forgotten:\n\n%s\n",
			len(offenders), spawnFile, strings.Join(offenders, "\n"))
	}
}

// gitSpawnRe matches a line that starts the git binary at ANY argument position - exec.Command,
// exec.CommandContext, and the osexec-aliased forms. It matches the whole line rather than what follows
// "exec.Command(" because the first version did exactly that and was blind to CommandContext(ctx, "git",
// ...), the form this package now uses everywhere. A gate that goes blind when the pattern it guards
// against becomes idiomatic is worse than no gate: it reports "clean". TestIsGitSpawn pins every form.
var gitSpawnRe = regexp.MustCompile(`exec\.Command(Context)?\([^)]*"git"`)

func isGitSpawn(line string) bool {
	return gitSpawnRe.MatchString(line)
}

// TestIsGitSpawn is the test for the matcher, not for the code: every form the codebase uses or is
// about to use must be detected, and one non-git spawn must NOT be, or the gate's silence means
// nothing.
func TestIsGitSpawn(t *testing.T) {
	mustCatch := []string{
		`cmd := exec.Command("git", args...)`,
		`cmd := exec.Command("git", "init", ".")`,
		`c := osexec.Command("git", args...)`,
		`cloneCmd := exec.CommandContext(ctx, "git", extCloneArgs(repo, srcDir)...)`,
		`cmd := osexec.CommandContext(ctx, "git", "-C", dir, "checkout", commit)`,
		`			cmd := exec.Command( "git", "x")`,
	}
	mustNotCatch := []string{
		`cmd := exec.Command("podman", "pull", imageRef)`,
		`cmd := exec.CommandContext(ctx, "podman", buildExtensionImageArgs(ref, dir)...)`,
		`// exec.Command("gitx" is only mentioned in a comment about something else`,
		`if gitSpawnRe.MatchString(line) {`,
	}
	for _, line := range mustCatch {
		if !isGitSpawn(line) {
			t.Errorf("isGitSpawn missed a git spawn form: %s", strings.TrimSpace(line))
		}
	}
	for _, line := range mustNotCatch {
		if isGitSpawn(line) {
			t.Errorf("isGitSpawn flagged a non-git line: %s", strings.TrimSpace(line))
		}
	}
}

// git reads GIT_AUTHOR_* and GIT_COMMITTER_* before it reads any config, so a value this process
// inherited outranks the -c arguments a commit carries. A command built with an Identity carries the
// pair it names and none of the ones it does not — an empty GIT_AUTHOR_NAME is not unset to git, it is
// an ident it refuses — and the fence still rides along.
func TestCommandWithIdentityReplacesTheInheritedIdentity(t *testing.T) {
	for _, name := range identityEnvNames {
		t.Setenv(name, "ambient-value")
	}
	origName, origEmail := AuthorIdentity()
	t.Cleanup(func() { SetAuthorIdentity(origName, origEmail) })

	for _, tc := range []struct {
		name        string
		serverName  string
		serverEmail string
		projectName string
		wantFlags   []string
		want        map[string]string
		absent      []string
	}{
		{
			name:        "a project naming only the name leaves the address to the repository",
			projectName: "Ada Lovelace",
			wantFlags:   []string{"-c", "user.name=Ada Lovelace"},
			want:        map[string]string{"GIT_AUTHOR_NAME": "Ada Lovelace", "GIT_COMMITTER_NAME": "Ada Lovelace"},
			absent:      []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"},
		},
		{
			name:        "the server's stamp answers the half the project left",
			serverEmail: "server@example.com",
			projectName: "Ada Lovelace",
			wantFlags:   []string{"-c", "user.name=Ada Lovelace", "-c", "user.email=server@example.com"},
			want: map[string]string{
				"GIT_AUTHOR_NAME": "Ada Lovelace", "GIT_COMMITTER_NAME": "Ada Lovelace",
				"GIT_AUTHOR_EMAIL": "server@example.com", "GIT_COMMITTER_EMAIL": "server@example.com",
			},
		},
		{
			name:   "nobody names anything, so nothing is inherited",
			absent: identityEnvNames,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			SetAuthorIdentity(tc.serverName, tc.serverEmail)
			cmd := CommandWithIdentity("", ProjectIdentity(tc.projectName, ""), "commit", "-m", "x")

			env := map[string]string{}
			for _, kv := range cmd.Env {
				key, value, ok := strings.Cut(kv, "=")
				if ok {
					// os/exec keeps the last occurrence of a duplicated name.
					env[key] = value
				}
			}
			for key, want := range tc.want {
				if env[key] != want {
					t.Errorf("%s = %q, want %q", key, env[key], want)
				}
			}
			for _, key := range tc.absent {
				if _, ok := env[key]; ok {
					t.Errorf("%s reached the child as %q; a half nobody named belongs to the repository", key, env[key])
				}
			}
			if !slices.Contains(cmd.Env, "GIT_CONFIG_COUNT=5") {
				t.Errorf("the execution fence left with the identity: %v", cmd.Env)
			}
			if tail := cmd.Args[len(cmd.Args)-3:]; !slices.Equal(tail, []string{"commit", "-m", "x"}) {
				t.Errorf("args = %v, want the subcommand last", cmd.Args)
			}
			if head := cmd.Args[1 : len(cmd.Args)-3]; !slices.Equal(head, tc.wantFlags) {
				t.Errorf("the identity ahead of the subcommand is %v, want %v", head, tc.wantFlags)
			}
		})
	}
}
