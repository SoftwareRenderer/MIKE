package updates

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"mike/backend/httputil"
)

// SchemaVersion is the manifest shape this build reads. A manifest naming any other schema is refused
// rather than partly read: a half-read one offers the wrong binary.
const SchemaVersion = 1

// maxManifestBytes bounds the release manifest: a version list outgrowing a few kilobytes is not one.
const maxManifestBytes = 64 << 10

// MaxPackageBytes bounds a release archive; the 8 MiB upstream cap is for API responses. The download is
// streamed to disk rather than read into memory, so this is where "not our tarball" becomes certain.
const MaxPackageBytes int64 = 512 << 20

var (
	// dottedVersion is what a release tag becomes after the leading "v". A binary whose version is
	// anything else cannot say what is newer.
	dottedVersion = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*$`)
	sha256Hex     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// assetKey is an "os/arch" pair as Go's runtime names it. The slash is required: a bare "amd64"
	// would never match a lookup.
	assetKey = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9.-]+$`)
)

// Asset is one platform's archive and the digest that names its bytes.
type Asset struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// Manifest is the release index: which version is current, and where each platform's archive is.
type Manifest struct {
	Schema    int              `json:"schema"`
	Latest    string           `json:"latest"`
	Published string           `json:"published"`
	NotesURL  string           `json:"notes_url,omitempty"`
	Assets    map[string]Asset `json:"assets"`
}

// strictManifest is the decode shape: every field present-or-absent is answerable, so a manifest that
// omits "latest" is a refusal naming it rather than an empty version that compares as older than
// everything.
type strictManifest struct {
	Schema    *int              `json:"schema"`
	Latest    *string           `json:"latest"`
	Published *string           `json:"published"`
	NotesURL  *string           `json:"notes_url"`
	Assets    *map[string]Asset `json:"assets"`
}

// ParseManifest decodes the release manifest and checks it against the URL it came from. The host rule is
// enforced here rather than left to the transport: both files must come from the host the operator named.
func ParseManifest(body []byte, from string) (*Manifest, error) {
	if len(body) > maxManifestBytes {
		return nil, fmt.Errorf("manifest is %d bytes, over the %d byte limit", len(body), maxManifestBytes)
	}
	var raw strictManifest
	dec := json.Unmarshal(body, &raw)
	if dec != nil {
		return nil, fmt.Errorf("manifest is not a JSON object: %w", dec)
	}
	if raw.Schema == nil {
		return nil, fmt.Errorf("manifest has no \"schema\"")
	}
	if *raw.Schema != SchemaVersion {
		return nil, fmt.Errorf("manifest schema %d is not the schema %d this build reads", *raw.Schema, SchemaVersion)
	}
	if raw.Latest == nil {
		return nil, fmt.Errorf("manifest has no \"latest\"")
	}
	want, err := normalizeVersion(*raw.Latest)
	if err != nil {
		return nil, fmt.Errorf("manifest \"latest\": %w", err)
	}

	host, err := manifestHost(from)
	if err != nil {
		return nil, err
	}

	m := &Manifest{Schema: *raw.Schema, Latest: want, Assets: map[string]Asset{}}
	if raw.Published != nil {
		m.Published = *raw.Published
	}
	if raw.NotesURL != nil && *raw.NotesURL != "" {
		notes, err := sameHostURL(*raw.NotesURL, host)
		if err != nil {
			return nil, fmt.Errorf("manifest \"notes_url\": %w", err)
		}
		m.NotesURL = notes
	}
	if raw.Assets == nil {
		return nil, fmt.Errorf("manifest has no \"assets\"")
	}
	for key, asset := range *raw.Assets {
		if !assetKey.MatchString(key) {
			return nil, fmt.Errorf("manifest asset %q is not an os/arch pair", key)
		}
		assetURL, err := sameHostURL(asset.URL, host)
		if err != nil {
			return nil, fmt.Errorf("manifest asset %q url: %w", key, err)
		}
		if !sha256Hex.MatchString(asset.SHA256) {
			return nil, fmt.Errorf("manifest asset %q carries a sha256 that is not 64 lowercase hex characters", key)
		}
		m.Assets[key] = Asset{URL: assetURL, SHA256: asset.SHA256}
	}
	return m, nil
}

// AssetFor returns this machine's archive, or false when the manifest names none for it — an answer about
// the release, not a fault.
func (m *Manifest) AssetFor(goos, goarch string) (Asset, bool) {
	a, ok := m.Assets[goos+"/"+goarch]
	return a, ok
}

// normalizeVersion accepts the dotted number a release carries, with or without its leading "v".
func normalizeVersion(v string) (string, error) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(v), "v")
	if !dottedVersion.MatchString(trimmed) {
		return "", fmt.Errorf("%q is not a dotted version", v)
	}
	return trimmed, nil
}

// KnownVersion reports whether a version string can be compared at all. "Cannot tell" is not "up to date".
func KnownVersion(v string) bool {
	_, err := normalizeVersion(v)
	return err == nil
}

// CompareVersions orders two dotted versions by segment, missing segments reading as zero, so 1.4 and
// 1.4.0 agree and 1.4.10 outranks 1.4.9.
func CompareVersions(a, b string) (int, error) {
	as, err := versionParts(a)
	if err != nil {
		return 0, err
	}
	bs, err := versionParts(b)
	if err != nil {
		return 0, err
	}
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x = as[i]
		}
		if i < len(bs) {
			y = bs[i]
		}
		if x != y {
			if x < y {
				return -1, nil
			}
			return 1, nil
		}
	}
	return 0, nil
}

func versionParts(v string) ([]int, error) {
	normalized, err := normalizeVersion(v)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(normalized, ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("%q is not a dotted version", v)
		}
		out[i] = n
	}
	return out, nil
}

// manifestHost is the host the manifest was fetched from, which is the only host its URLs may name.
func manifestHost(from string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(from))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("manifest URL %q has no host", from)
	}
	return strings.ToLower(u.Host), nil
}

// sameHostURL accepts an https URL, or a plain-HTTP one on loopback, whose host is the manifest's own.
// One rule for the archive and the notes, because both are read out of the same document.
func sameHostURL(raw, host string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("URL is empty")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%q is not a URL", raw)
	}
	if !httputil.FetchSchemeAllowed(u) {
		return "", fmt.Errorf("%q must be https (http is accepted only on loopback)", raw)
	}
	if strings.ToLower(u.Host) != host {
		return "", fmt.Errorf("%q names the host %q, and only the manifest's own host %q is trusted", raw, u.Host, host)
	}
	return raw, nil
}
