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
)

// RegistryClient fetches extension metadata and manages container images.
type RegistryClient struct {
	httpClient *http.Client
}

// NewRegistryClient creates a new RegistryClient.
func NewRegistryClient() *RegistryClient {
	return &RegistryClient{
		httpClient: &http.Client{},
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

// fetchContent fetches content from a URL, supporting both HTTP(S) and file:// schemes.
func (rc *RegistryClient) fetchContent(url string) (io.ReadCloser, error) {
	if strings.HasPrefix(url, "file://") {
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

// FetchManifest downloads a single extension manifest.
func (rc *RegistryClient) FetchManifest(url string) (ExtensionManifest, error) {
	body, err := rc.fetchContent(url)
	if err != nil {
		return ExtensionManifest{}, fmt.Errorf("fetch manifest: %w", err)
	}
	defer body.Close()

	var manifest ExtensionManifest
	if err := json.NewDecoder(body).Decode(&manifest); err != nil {
		return ExtensionManifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	return manifest, nil
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
	cmd := exec.Command("podman", "pull", imageRef)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pull image %s: %w (output: %s)", imageRef, err, string(output))
	}
	return nil
}

// BuildImage builds an image from a Dockerfile path via podman.
func (rc *RegistryClient) BuildImage(dockerfilePath, imageRef string) error {
	cmd := exec.Command("podman", "build", "-t", imageRef, "-f", dockerfilePath, ".")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build image %s from %s: %w (output: %s)", imageRef, dockerfilePath, err, string(output))
	}
	return nil
}

// BuildImageFromReader builds an image from a Dockerfile content via podman.
func (rc *RegistryClient) BuildImageFromReader(dockerfileContent, imageRef string) error {
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

// StreamBuildOutput builds an image and streams output via a channel.
func (rc *RegistryClient) StreamBuildOutput(ctx context.Context, dockerfilePath, imageRef string, ch chan<- string) error {
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
