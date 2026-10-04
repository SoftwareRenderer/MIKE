package sandboxruntime

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestPodmanEnvNeverCarriesServerSecrets(t *testing.T) {
	secrets := map[string]string{
		"MIKE_ENCRYPTION_KEY":   "the-key-that-decrypts-every-provider",
		"DANGER_ZONE":           "1",
		"MIKE_LISTEN":           "0.0.0.0",
		"MIKE_ALLOWED_HOSTS":    "mike.example",
		"MIKE_REGISTRY_URL":     "https://attacker.example/registry.json",
		"MIKE_EXT_MOUNT_ROOTS":  "/etc",
		"MIKE_TRUST_PROXY":      "1",
		"MIKE_TLS":              "1",
		"AWS_SECRET_ACCESS_KEY": "shhh",
		"OPENAI_API_KEY":        "sk-shhh",
	}
	for k, v := range secrets {
		t.Setenv(k, v)
	}

	for _, kv := range PodmanEnv() {
		name, _, _ := strings.Cut(kv, "=")
		if _, isSecret := secrets[name]; isSecret {
			t.Errorf("PodmanEnv() leaked %s to the podman CLI", name)
		}
		if strings.HasPrefix(name, "MIKE_") {
			t.Errorf("PodmanEnv() leaked MIKE_* configuration variable %s", name)
		}
	}
}

// TestPodmanEnvKeepsWhatRootlessPodmanNeeds pins the variables whose absence breaks sandboxing in a
// way that reads as a podman bug rather than a stripped environment. Each has a reason in the map
// comment; if one is removed, this test says which one and the comment says why it was there.
func TestPodmanEnvKeepsWhatRootlessPodmanNeeds(t *testing.T) {
	needed := map[string]string{
		"PATH":                     "/usr/bin",
		"HOME":                     "/home/operator",
		"XDG_RUNTIME_DIR":          "/run/user/1000",
		"DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/user/1000/bus",
		"TMPDIR":                   "/tmp",
		"REGISTRY_AUTH_FILE":       "/home/operator/.config/containers/auth.json",
		"XDG_DATA_HOME":            "/home/operator/.local/share",
		"XDG_CONFIG_HOME":          "/home/operator/.config",
		"SSL_CERT_FILE":            "/etc/ssl/certs/internal-ca.pem",
		"HTTPS_PROXY":              "http://proxy.internal:3128",
		"LC_ALL":                   "C.UTF-8",
		"CONTAINERS_CONF":          "/home/operator/.config/containers/containers.conf",
		"PODMAN_TMPDIR":            "/var/tmp",
		"DOCKER_AUTH_CONFIG":       "{}",
	}
	for k, v := range needed {
		t.Setenv(k, v)
	}

	got := strings.Join(PodmanEnv(), "\n")
	for k, v := range needed {
		if !strings.Contains(got, k+"="+v) {
			t.Errorf("PodmanEnv() dropped %s — rootless podman needs it; see the map comment", k)
		}
	}
}

func TestPodmanEnvDropsEmptyValues(t *testing.T) {
	// An empty XDG_RUNTIME_DIR is fatal to podman ("cannot find runtime directory") where an
	// absent one falls back. Passing through "" is therefore worse than not passing it.
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("HOME", "/home/operator")

	for _, kv := range PodmanEnv() {
		if kv == "XDG_RUNTIME_DIR=" {
			t.Error("PodmanEnv() emitted an empty XDG_RUNTIME_DIR= instead of omitting it")
		}
	}
}

func TestPodmanEnvExtrasWin(t *testing.T) {
	// os/exec keeps the LAST occurrence of a duplicated key, so extras go last.
	t.Setenv("CONTAINERS_CONF", "/from/server")
	env := PodmanEnv("CONTAINERS_CONF=/from/caller")

	if env[len(env)-1] != "CONTAINERS_CONF=/from/caller" {
		t.Errorf("extras must be appended last so os/exec prefers them; env tail = %v", env[max(0, len(env)-3):])
	}
}

func TestPodmanCommandSetsEnvAndWaitDelay(t *testing.T) {
	cmd := PodmanCommand("ps")
	if cmd.Env == nil {
		t.Fatal("cmd.Env is nil: os/exec hands the podman CLI the whole server environment")
	}
	if cmd.WaitDelay != PodmanWaitDelay {
		t.Errorf("WaitDelay = %v, want %v", cmd.WaitDelay, PodmanWaitDelay)
	}
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "MIKE_ENCRYPTION_KEY=") {
			t.Fatal("cmd.Env carries MIKE_ENCRYPTION_KEY")
		}
	}
}

// TestPodmanCommandIsTheOnlySpawnPoint is the podman counterpart of backend/git's
// TestNoGitSubprocessSpawnsOutsideGit. There were five podman spawn points and one of them
// (RegistryClient.PullImage) had already drifted out of the "every podman subprocess carries
// WaitDelay" rule; a source walk is what keeps the rule from drifting again.
func TestPodmanCommandIsTheOnlySpawnPoint(t *testing.T) {
	root := moduleRoot(t)

	var offenders []string
	for _, sub := range []string{"backend", "pkg"} {
		walkGoFiles(t, root, sub, func(path, line string) {
			if !podmanSpawnRe.MatchString(line) {
				return
			}
			// The constructor itself, and the runsc probe, which cannot use it because the
			// constructor pins the binary to podman.
			if strings.HasSuffix(path, "pkg/sandboxruntime/podmanenv.go") ||
				strings.HasSuffix(path, "pkg/sandboxruntime/runtime.go") {
				return
			}
			offenders = append(offenders, path+": "+strings.TrimSpace(line))
		})
	}

	if len(offenders) > 0 {
		t.Errorf("%d podman subprocess(es) constructed outside sandboxruntime.PodmanCommand. Use it so "+
			"the environment allowlist and WaitDelay cannot be forgotten:\n\n%s\n",
			len(offenders), strings.Join(offenders, "\n"))
	}
}

func TestIsPodmanSpawnDetection(t *testing.T) {
	for _, line := range []string{
		`c := exec.Command("podman", args...)`,
		`cmd := exec.CommandContext(ctx, "podman", "pull", ref)`,
		`agentCmd := exec.Command("podman", "exec", "-i", name)`,
	} {
		if !podmanSpawnRe.MatchString(line) {
			t.Errorf("podmanSpawnRe missed: %s", line)
		}
	}
	for _, line := range []string{
		`cmd := exec.Command("git", "commit")`,
		`cmd := exec.Command("runsc", "--version")`,
	} {
		if podmanSpawnRe.MatchString(line) {
			t.Errorf("podmanSpawnRe flagged a non-podman line: %s", line)
		}
	}
}

// podmanSpawnRe matches any line that starts the podman CLI, at any argument position, with or
// without a context.
var podmanSpawnRe = regexp.MustCompile(`exec\.Command(Context)?\([^)]*"podman"`)

// moduleRoot and walkGoFiles are duplicated from backend/git's env_test on purpose: a package cannot
// import another package's tests, and factoring them out would mean a test-support package that
// production code could import, which is its own hazard.
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

// notOurTree is scoped RELATIVE TO THE ROOT, not by base name — matching a bare name would also skip
// backend/extensions/, hiding real code behind an unrelated top-level directory.
var notOurTree = []string{".worktrees", "node_modules", "extensions", "vendor", "dist"}

func skipRel(rel string) bool {
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
	return strings.HasPrefix(first, ".")
}

func walkGoFiles(t *testing.T, root, sub string, visit func(path, line string)) {
	t.Helper()
	err := filepath.WalkDir(filepath.Join(root, sub), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			if skipRel(rel) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			visit(filepath.ToSlash(rel), sc.Text())
		}
		return sc.Err()
	})
	if err != nil {
		t.Fatalf("walk %s: %v", sub, err)
	}
}

// The slow-child report is the only thing an operator sees when podman wedges, so the name it prints has
// to identify the operation. Flags and image references are excluded: the first verbs are what a human
// scans for, and the long arguments are already in the caller's own error text.
func TestPodmanLabelNamesTheOperation(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"inspect", "kanban-sandbox-frost-amber-quartz"}, "podman inspect kanban-sandbox-frost-amber-quartz"},
		{[]string{"rm", "-f", "kanban-sandbox-x"}, "podman rm kanban-sandbox-x"},
		{[]string{"create", "--runtime", "runsc", "--name", "c", "img"}, "podman create runsc"},
		{[]string{"runsc", "--version"}, "podman runsc"},
		{[]string{"--version"}, "podman"},
	}
	for _, tt := range tests {
		if got := podmanLabel(tt.args); got != tt.want {
			t.Errorf("podmanLabel(%q) = %q, want %q", tt.args, got, tt.want)
		}
	}
}
