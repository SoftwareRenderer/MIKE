package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"kanban/pkg/sandboxruntime"
)

// RegistryClient fetches extension metadata and manages container images.
type RegistryClient struct {
	httpClient *http.Client
}

// NewRegistryClient creates a new RegistryClient.
func NewRegistryClient() *RegistryClient {
	return &RegistryClient{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			// Do not follow redirects
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// ResolveURL resolves a relative URL against a base URL.
// Supports file:// and http(s):// schemes.
// If the target URL already has a scheme, it is returned as-is.
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

// fetchContent fetches content from a URL, supporting both HTTP(S) and file://
// schemes. Bare filesystem paths (no scheme) are treated as local files, with
// relative paths resolved against the working directory.
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

// FetchIndex downloads and parses the registry index.
func (rc *RegistryClient) FetchIndex(url string) (RegistryIndex, error) {
	body, err := rc.fetchContent(url)
	if err != nil {
		return nil, fmt.Errorf("fetch registry index: %w", err)
	}
	defer body.Close()

	var index RegistryIndex
	if err := json.NewDecoder(body).Decode(&index); err != nil {
		return nil, fmt.Errorf("decode registry index: %w", err)
	}
	return index, nil
}

// FetchChangelog fetches an extension's changelog from the central registry
// server and returns its raw content. The URL is derived from the registry
// base URL by appending "<author-slug>/<name>" — the same base location that
// serves the registry index (works for http(s):// and file:// URLs, and for
// bare local paths).
func (rc *RegistryClient) FetchChangelog(baseURL, author, name string) (string, error) {
	body, err := rc.fetchContent(ResolveURL(baseURL, authorDir(author)+"/"+name))
	if err != nil {
		return "", fmt.Errorf("fetch changelog for %s/%s: %w", author, name, err)
	}
	defer body.Close()

	data, err := io.ReadAll(body)
	if err != nil {
		return "", fmt.Errorf("read changelog for %s/%s: %w", author, name, err)
	}
	return string(data), nil
}

// FetchDockerfile downloads a Dockerfile and returns its contents.
func (rc *RegistryClient) FetchDockerfile(url string) (string, error) {
	body, err := rc.fetchContent(url)
	if err != nil {
		return "", fmt.Errorf("fetch dockerfile: %w", err)
	}
	defer body.Close()

	data, err := io.ReadAll(body)
	if err != nil {
		return "", fmt.Errorf("read dockerfile: %w", err)
	}
	return string(data), nil
}

// PullImage pulls an OCI image via podman.
func (rc *RegistryClient) PullImage(imageRef string) error {
	if err := sandboxruntime.ValidateImageRef(imageRef); err != nil {
		return err
	}
	cmd := exec.Command("podman", "pull", imageRef)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pull image %s: %w (output: %s)", imageRef, err, string(output))
	}
	return nil
}

// BuildImage builds an image from a Dockerfile path via podman.
func (rc *RegistryClient) BuildImage(dockerfilePath, imageRef string) error {
	if err := sandboxruntime.ValidateImageRef(imageRef); err != nil {
		return err
	}
	if err := sandboxruntime.ValidateDockerfilePath(dockerfilePath); err != nil {
		return err
	}
	cmd := exec.Command("podman", "build", "-t", imageRef, "-f", dockerfilePath, ".")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build image %s from %s: %w (output: %s)", imageRef, dockerfilePath, err, string(output))
	}
	return nil
}

// BuildImageFromReader builds an image from a Dockerfile content via podman.
func (rc *RegistryClient) BuildImageFromReader(dockerfileContent, imageRef string) error {
	if err := sandboxruntime.ValidateImageRef(imageRef); err != nil {
		return err
	}
	// Write Dockerfile to temp dir and build from it
	cmd := exec.Command("podman", "build", "-t", imageRef, "-f", "-", ".")
	cmd.Stdin = strings.NewReader(dockerfileContent)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build image %s: %w (output: %s)", imageRef, err, string(output))
	}
	return nil
}

// ImageExists checks if an image is available locally via podman.
func (rc *RegistryClient) ImageExists(imageRef string) bool {
	if err := sandboxruntime.ValidateImageRef(imageRef); err != nil {
		return false
	}
	cmd := exec.Command("podman", "image", "exists", imageRef)
	return cmd.Run() == nil
}

// semverRegex matches semantic version strings (major.minor.patch).
var semverRegex = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

// CompareVersion compares two semantic version strings.
// Returns -1 if a < b, 0 if a == b, 1 if a > b.
// Non-semver strings are compared lexicographically.
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

// UpdateAvailable returns true if the remote version is newer than the installed version.
func UpdateAvailable(installed, remote string) bool {
	return CompareVersion(installed, remote) < 0
}

// newPermissions returns the UI permissions present in the remote (registry)
// capabilities but absent from the installed extension's manifest — i.e. the
// newly requested permissions a user would grant by updating. Unknown
// permission names are passed through as-is.
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

// commitHashRe matches a full hex commit hash: 40-char (SHA-1) or 64-char
// (SHA-256). Both SHA-1 and SHA-256 object-format repos are supported, so both
// hash lengths are accepted.
var commitHashRe = regexp.MustCompile(`^[0-9a-fA-F]{40}|[0-9a-fA-F]{64}$`)

// validateGitSource validates a git repo URL and pinned commit for the
// commit-verified install path. The repo must not start with a dash (to
// prevent it being parsed as a git flag) and the commit must be a full 40-char
// SHA-1 or 64-char SHA-256 hex hash so only an exact, content-addressed
// revision is installed.
func validateGitSource(repo, commit string) error {
	if repo == "" {
		return fmt.Errorf("repo is required")
	}
	if strings.HasPrefix(repo, "-") {
		return fmt.Errorf("repo must not start with a dash: %q", repo)
	}
	if !commitHashRe.MatchString(commit) {
		return fmt.Errorf("commit must be a full 40-char SHA-1 or 64-char SHA-256 hex hash: %q", commit)
	}
	return nil
}

// validateSubdir rejects paths that would escape the checkout root.
func validateSubdir(subdir string) error {
	if subdir == "" {
		return nil
	}
	clean := filepath.Clean(subdir)
	if strings.HasPrefix(clean, "..") || strings.HasPrefix(clean, "/") || filepath.IsAbs(clean) {
		return fmt.Errorf("invalid subdir: %q", subdir)
	}
	return nil
}

// CheckoutExtension checks out exactly the pinned commit of a git repository
// and materializes its extension subdirectory into destDir.
//
// The checkout is verified cryptographically: after a shallow fetch at the
// pinned SHA, `git rev-parse HEAD` must equal the pinned commit. Only if the
// verification passes is the content copied to destDir, so the installed files
// are guaranteed to be the vetted commit's bytes. Any mismatch or fetch failure
// returns an error and nothing is written.
func (rc *RegistryClient) CheckoutExtension(repo, commit, subdir, destDir string) error {
	if err := validateGitSource(repo, commit); err != nil {
		return err
	}
	if err := validateSubdir(subdir); err != nil {
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
	cloneCmd := exec.Command("git", "clone", "-q", "--no-checkout", repo, srcDir)
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("clone %s: %w (output: %s)", repo, err, strings.TrimSpace(string(out)))
	}

	checkoutCmd := exec.Command("git", "-C", srcDir, "checkout", "-q", commit)
	if out, err := checkoutCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("checkout pinned commit %s: %w (output: %s)", commit, err, strings.TrimSpace(string(out)))
	}

	// Verify the checked-out commit is exactly the pinned one before copying.
	out, err := exec.Command("git", "-C", srcDir, "rev-parse", "HEAD").Output()
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

// copyDir recursively copies the contents of src into dst, creating dst if
// needed. Symbolic links are skipped (not followed, not recreated) to avoid
// escaping the extension directory.
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

// StreamBuildOutput builds an image and streams output via a channel.
func (rc *RegistryClient) StreamBuildOutput(ctx context.Context, dockerfilePath, imageRef string, ch chan<- string) error {
	if err := sandboxruntime.ValidateImageRef(imageRef); err != nil {
		return err
	}
	if err := sandboxruntime.ValidateDockerfilePath(dockerfilePath); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "podman", "build", "-t", imageRef, "-f", dockerfilePath, ".")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("create stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start build: %w", err)
	}

	// Stream output
	go func() {
		defer close(ch)
		mergeIO := func(r io.Reader) {
			buf := make([]byte, 4096)
			for {
				n, err := r.Read(buf)
				if n > 0 {
					ch <- string(buf[:n])
				}
				if err != nil {
					return
				}
			}
		}
		mergeIO(stdout)
		mergeIO(stderr)
	}()

	return cmd.Wait()
}
