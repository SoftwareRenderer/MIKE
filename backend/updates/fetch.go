package updates

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"mike/backend/httputil"
)

// Fetcher is how this package reaches the network, so every decision below is testable without one.
// A manifest is small and parsed; an archive is not and must never be held in memory.
type Fetcher interface {
	Get(ctx context.Context, url string) ([]byte, error)
	// Download writes the URL's body to dest and returns the byte count and the digest of those bytes.
	// It refuses past limit without writing the excess.
	Download(ctx context.Context, url, dest string, limit int64) (int64, string, error)
}

// maxManifestFetch mirrors maxManifestBytes at the transport: the cap runs before the bytes are read.
const maxManifestFetch = maxManifestBytes

// manifestTimeout bounds a manifest fetch: nobody is watching this request finish.
const manifestTimeout = 20 * time.Second

// downloadTimeout bounds an archive fetch — generous, because the caller is a person waiting on it.
const downloadTimeout = 10 * time.Minute

// httpFetcher is the production Fetcher. The host of every URL fetched is granted to the egress client:
// it is a host the operator named, which is the only way a loopback address is ever dialled here.
// Nothing else in the package can introduce a host — ParseManifest refuses a URL on any other.
type httpFetcher struct{}

func (httpFetcher) Get(ctx context.Context, rawURL string) ([]byte, error) {
	host, err := egressHost(rawURL)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, manifestTimeout)
	defer cancel()
	answer, err := httputil.DoOutbound(ctx, httputil.OutboundRequest{
		URL:          rawURL,
		GrantedHosts: []string{host},
	})
	if err != nil {
		return nil, err
	}
	if !answer.OK() {
		return nil, fmt.Errorf("%s answered %d", rawURL, answer.StatusCode)
	}
	if int64(len(answer.Body)) > maxManifestFetch {
		return nil, fmt.Errorf("%s sent %d bytes, over the %d byte manifest limit", rawURL, len(answer.Body), maxManifestFetch)
	}
	return answer.Body, nil
}

func (httpFetcher) Download(ctx context.Context, rawURL, dest string, limit int64) (int64, string, error) {
	host, err := egressHost(rawURL)
	if err != nil {
		return 0, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	return httputil.DownloadToFileGranting(ctx, rawURL, dest, limit, []string{host})
}

// egressHost is the scheme rule and the host a fetch may dial. The client itself belongs to httputil.
func egressHost(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%q is not a URL", rawURL)
	}
	if !httputil.FetchSchemeAllowed(u) {
		return "", fmt.Errorf("%q must be https (http is accepted only on loopback)", rawURL)
	}
	return u.Hostname(), nil
}
