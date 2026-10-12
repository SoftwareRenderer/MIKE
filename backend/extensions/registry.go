package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"mike/backend/fsutil"
	"mike/backend/git"
	"mike/pkg/sandboxruntime"
	"mike/pkg/subproc"

	"mike/backend/httputil"
	"mike/pkg/logging"
)

type RegistryClient struct {
	httpClient *http.Client
	// rt qualifies image refs before they reach podman and owns the local-storage probe, so this
	// client doesn't re-implement either (see pkg/sandboxruntime/image_ref.go).
	rt *sandboxruntime.Runtime
}

// NewRegistryClient creates a client that owns no host decision: the bridge binary it stages into a
// build context is found in the normal places.
func NewRegistryClient() *RegistryClient {
	return NewRegistryClientWithBoot(sandboxruntime.Boot{})
}

// NewRegistryClientWithBoot creates a client whose builds stage the bridge from boot.AgentPath when it
// names one. An image build has to COPY sandbox/sandbox-bridge into its context and the binary is not
// in the working directory, so the wired path has to reach here; the server wires it from startup.Config.
func NewRegistryClientWithBoot(boot sandboxruntime.Boot) *RegistryClient {
	return &RegistryClient{
		rt: sandboxruntime.NewRuntimeWithBoot(boot),
		// A registry URL can come from MIKE_REGISTRY_URL, and a manifest's asset URLs are relative
		// to whatever it resolved against: no redirect is followed, so nothing reaches a host the
		// operator did not name.
		httpClient: httputil.NewSingleAttemptClient(30 * time.Second),
	}
}

func ResolveURL(base, target string) string {
	// If target already has a scheme, return as-is
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "file://") {
		return target
	}

	// For file:// base URLs, resolve as file paths
	if strings.HasPrefix(base, "file://") {
		basePath := strings.TrimPrefix(base, "file://")
		// Remove the filename to get the directory
		baseDir := filepath.Dir(basePath)
		resolved := filepath.Join(baseDir, target)
		return "file://" + resolved
	}

	// For http(s):// base URLs, resolve using URL parsing
	baseURL, err := url.Parse(base)
	if err != nil {
		return target // Fallback to target as-is
	}
	targetURL, err := url.Parse(target)
	if err != nil {
		return target // Fallback to target as-is
	}
	resolved := baseURL.ResolveReference(targetURL)
	return resolved.String()
}

func (rc *RegistryClient) fetchContent(url string) (io.ReadCloser, error) {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		path := strings.TrimPrefix(url, "file://")
		// Resolve relative to working directory
		if !filepath.IsAbs(path) {
			abs, err := filepath.Abs(path)
			if err != nil {
				return nil, fmt.Errorf("resolve file path %s: %w", path, err)
			}
			path = abs
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open file %s: %w", path, err)
		}
		return f, nil
	}

	resp, err := rc.httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}

func (rc *RegistryClient) FetchIndex(url string) (RegistryIndex, error) {
	body, err := rc.fetchContent(url)
	if err != nil {
		return nil, fmt.Errorf("fetch registry index: %w", err)
	}
	defer body.Close()

	// Read before parsing: decoding off the body reads as much as the sender streams.
	data, err := httputil.ReadUpstream(body)
	if err != nil {
		return nil, fmt.Errorf("read registry index: %w", err)
	}
	var index RegistryIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, fmt.Errorf("decode registry index: %w", err)
	}
	return index, nil
}

func (rc *RegistryClient) FetchChangelog(baseURL, author, name string) (string, error) {
	body, err := rc.fetchContent(ResolveURL(baseURL, authorDir(author)+"/"+name))
	if err != nil {
		return "", fmt.Errorf("fetch changelog for %s/%s: %w", author, name, err)
	}
	defer body.Close()

	data, err := httputil.ReadUpstream(body)
	if err != nil {
		return "", fmt.Errorf("read changelog for %s/%s: %w", author, name, err)
	}
	return string(data), nil
}

func (rc *RegistryClient) FetchDockerfile(url string) (string, error) {
	body, err := rc.fetchContent(url)
	if err != nil {
		return "", fmt.Errorf("fetch dockerfile: %w", err)
	}
	defer body.Close()

	data, err := httputil.ReadUpstream(body)
	if err != nil {
		return "", fmt.Errorf("read dockerfile: %w", err)
	}
	return string(data), nil
}

// ExtensionImageRef is the image name an extension's own Dockerfile is built under. It is qualified
// with the local registry because that is where podman stores a local build, and a short name would
// put every later pull/exists/create through short-name resolution (see image_ref.go).
func ExtensionImageRef(name, version string) string {
	return sandboxruntime.QualifyLocalImageRef(fmt.Sprintf("kanban-ext-%s:%s", name, version))
}

// ValidateImage is the rule for an image reference someone else chose (a registry entry's image, a
// project's configured sandbox image): it must name its registry.
func (rc *RegistryClient) ValidateImage(imageRef string) error {
	return sandboxruntime.ValidateConfiguredImageRef(imageRef)
}

// pullSlowReport and buildSlowReport are the thresholds past which a network pull or an image build is
// worth a log line. Both are deliberately longer than subproc.SlowWait: their ordinary path is minutes
// long, and a warning that fires on the ordinary path teaches an operator to ignore it.
const (
	pullSlowReport  = 2 * time.Minute
	buildSlowReport = 5 * time.Minute
)

func warnSlowPodman(format string, args ...any) {
	logging.For(logging.Extensions).Warnf(format, args...)
}

func (rc *RegistryClient) PullImage(imageRef string) error {
	if err := sandboxruntime.ValidateImageRef(imageRef); err != nil {
		return err
	}
	imageRef = rc.rt.ResolveImageRef(imageRef)
	// PodmanCommand, not exec.Command: this is the site that had no WaitDelay at all, so a pull that
	// wedged on a registry that accepts the connection but never answers held cmd.Wait open
	// indefinitely — and it inherited the server's whole environment.
	cmd := sandboxruntime.PodmanCommand("pull", imageRef)
	// A pull is expected to take minutes on a slow registry, so its report threshold is well past that;
	// the line means "this is still the same pull", which is the difference between a slow install and a
	// hung one when the progress stream has gone quiet.
	stop := subproc.Watch("podman pull "+imageRef, pullSlowReport, warnSlowPodman)
	output, err := cmd.CombinedOutput()
	stop()
	if err != nil {
		return fmt.Errorf("pull image %s: %w (output: %s)", imageRef, err, string(output))
	}
	return nil
}

func (rc *RegistryClient) ImageExists(imageRef string) bool {
	return rc.rt.ImageExists(imageRef)
}

// semverRegex matches semantic version strings (major.minor.patch).
var semverRegex = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

func CompareVersion(a, b string) int {
	matchA := semverRegex.FindStringSubmatch(a)
	matchB := semverRegex.FindStringSubmatch(b)

	// If both are valid semver, compare numerically
	if matchA != nil && matchB != nil {
		for i := 1; i <= 3; i++ {
			var va, vb int
			fmt.Sscanf(matchA[i], "%d", &va)
			fmt.Sscanf(matchB[i], "%d", &vb)
			if va != vb {
				if va < vb {
					return -1
				}
				return 1
			}
		}
		return 0
	}

	// Fall back to lexicographic comparison
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func UpdateAvailable(installed, remote string) bool {
	return CompareVersion(installed, remote) < 0
}

func newPermissions(installed, remote *Capabilities) []string {
	if remote == nil || remote.UI == nil || len(remote.UI.Permissions) == 0 {
		return nil
	}
	current := make(map[string]struct{})
	if installed != nil && installed.UI != nil {
		for _, p := range installed.UI.Permissions {
			current[p] = struct{}{}
		}
	}
	added := make([]string, 0, len(remote.UI.Permissions))
	for _, p := range remote.UI.Permissions {
		if _, ok := current[p]; !ok {
			added = append(added, p)
		}
	}
	return added
}

func newEgressHosts(installed, remote *Capabilities) []string {
	network := remote.GetNetwork()
	if network == nil {
		return nil
	}
	current := egressHostSet(installed.GetNetwork())
	var added []string
	for _, host := range network.AllowedDomains {
		key := normalizeEgressHost(host)
		if key == "" {
			continue
		}
		if _, ok := current[key]; ok {
			continue
		}
		current[key] = struct{}{}
		added = append(added, key)
	}
	sort.Strings(added)
	return added
}

func egressHostSet(network *NetworkConfig) map[string]struct{} {
	if network == nil {
		return map[string]struct{}{}
	}
	set := make(map[string]struct{}, len(network.AllowedDomains))
	for _, host := range network.AllowedDomains {
		if key := normalizeEgressHost(host); key != "" {
			set[key] = struct{}{}
		}
	}
	return set
}

func normalizeEgressHost(host string) string {
	return strings.ToLower(strings.TrimSpace(host))
}

// commitHashRe matches a full hex commit hash: 40-char (SHA-1) or 64-char
// (SHA-256), since both object formats are supported. Both alternatives are
// anchored — an unanchored alternation would accept 40 hex characters followed by
// anything, which is not a pinned revision.
var commitHashRe = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)

// validateGitSource validates a repo URL and pinned commit for the commit-verified install
// path. The repo is registry content and the clone runs on the host, so it must pass the same
// gate as a user-supplied clone (git.ValidateCloneURL) — see extCloneArgs for what a helper
// transport would do. The commit must be a full hex hash, so only an exact, content-addressed
// revision is installed.
func validateGitSource(repo, commit string) error {
	if err := git.ValidateCloneURL(repo); err != nil {
		return fmt.Errorf("repo %q: %w", repo, err)
	}
	if !commitHashRe.MatchString(commit) {
		return fmt.Errorf("commit must be a full 40-char SHA-1 or 64-char SHA-256 hex hash: %q", commit)
	}
	return nil
}

// extCloneArgs builds the clone argv for an extension checkout: the transport
// fence first (it must reach the process that parses the URL), then this flow's
// flags. repo must already have passed validateGitSource.
func extCloneArgs(repo, dst string) []string {
	return append(git.CloneProtocolArgs(), "clone", "-q", "--no-checkout", repo, dst)
}

// gitErrorOutput summarises git output for an error returned to the caller: one
// line, bounded. The full output is logged; a client gets enough to act on
// ("repository not found", "transport not allowed") without this becoming a
// channel for whatever a subprocess printed.
func gitErrorOutput(out []byte) string {
	const max = 300
	line := strings.Join(strings.Fields(string(out)), " ")
	if len(line) > max {
		return line[:max] + "…"
	}
	return line
}

func validateSubdir(subdir string) error {
	if subdir == "" {
		return nil
	}
	clean := filepath.Clean(subdir)
	if filepath.IsAbs(clean) || !fsutil.RelInsideRoot(clean) {
		return fmt.Errorf("invalid subdir: %q", subdir)
	}
	return nil
}

// CheckoutExtension checks out exactly the pinned commit of a git repository and materializes
// its extension subdirectory into destDir. The checkout is verified cryptographically: after a
// shallow fetch at the pinned SHA, `git rev-parse HEAD` must equal that commit, and only then is
// content copied — so installed files are the vetted commit's bytes. Any mismatch or fetch
// failure returns an error and writes nothing.
func (rc *RegistryClient) CheckoutExtension(repo, commit, subdir, destDir string) error {
	return rc.CheckoutExtensionContext(context.Background(), repo, commit, subdir, destDir)
}

// CheckoutExtensionContext is CheckoutExtension under a deadline. The verification is identical; only
// the three git subprocesses gain a context, so a fetch that hangs (an unreachable host, a server that
// accepts the connection and says nothing) cannot outlive the caller's deadline. The startup repair
// runs it under one precisely because nothing there may hang: an install the user asked for can wait,
// a boot cannot.
func (rc *RegistryClient) CheckoutExtensionContext(ctx context.Context, repo, commit, subdir, destDir string) error {
	if err := validateGitSource(repo, commit); err != nil {
		return err
	}
	if err := validateSubdir(subdir); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	tmp, err := os.MkdirTemp("", "kanban-ext-checkout-*")
	if err != nil {
		return fmt.Errorf("create checkout dir: %w", err)
	}
	defer os.RemoveAll(tmp)

	// A full clone (no checkout) auto-detects the repository's object format
	// (SHA-1 or SHA-256) and fetches every ref, so any commit reachable from a
	// branch or tag is present. The pinned commit must therefore be on a
	// permanent ref.
	srcDir := filepath.Join(tmp, "src")
	cloneCmd := git.CommandContext(ctx, "", extCloneArgs(repo, srcDir)...)
	// The fence in the argv is half of it: GIT_ALLOW_PROTOCOL is enforced by git
	// itself whatever the host's git config says, and GIT_TERMINAL_PROMPT=0 stops
	// a credential prompt hanging the install request. See git.CloneEnv.
	cloneCmd.Env = git.CloneEnv()
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		logging.For(logging.Extensions).Warnf("clone %s failed: %v: %s", repo, err, out)
		return fmt.Errorf("clone %s: %w (output: %s)", repo, err, gitErrorOutput(out))
	}

	checkoutCmd := git.CommandContext(ctx, "", "-C", srcDir, "checkout", "-q", commit)
	if out, err := checkoutCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("checkout pinned commit %s: %w (output: %s)", commit, err, strings.TrimSpace(string(out)))
	}

	// Verify the checked-out commit is exactly the pinned one before copying.
	out, err := git.CommandContext(ctx, "", "-C", srcDir, "rev-parse", "HEAD").Output()
	if err != nil {
		return fmt.Errorf("verify checkout: %w", err)
	}
	head := strings.TrimSpace(string(out))
	if !strings.EqualFold(head, commit) {
		return fmt.Errorf("commit verification failed: checkout resolved to %s, want %s", head, commit)
	}

	src := srcDir
	if subdir != "" {
		src = filepath.Join(srcDir, subdir)
	}

	// Clear any existing install before copying the freshly verified checkout.
	// This runs only after verification succeeded, so a failed/malicious checkout
	// never touches the destination. Removing first also drops stale files that
	// were deleted in the newly vetted revision.
	if err := os.RemoveAll(destDir); err != nil {
		return fmt.Errorf("clear existing install %s: %w", destDir, err)
	}
	if err := copyDir(src, destDir); err != nil {
		return fmt.Errorf("materialize extension: %w", err)
	}
	return nil
}

// copyDir recursively copies the contents of src into dst.
// Symbolic links are skipped (not followed, not recreated) to avoid escaping the extension directory.
func copyDir(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", src)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		// Skip the git metadata directory. It exists when src is the root of a
		// checked-out repo (e.g. a blank subdir), and installing it would drag
		// the full object store and history into the extension directory.
		if e.Name() == ".git" {
			continue
		}
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		switch {
		case e.IsDir():
			if err := copyDir(s, d); err != nil {
				return err
			}
		case e.Type()&os.ModeSymlink != 0:
			// Skip symlinks — never follow or recreate them.
		default:
			data, err := os.ReadFile(s)
			if err != nil {
				return err
			}
			if err := os.WriteFile(d, data, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}
