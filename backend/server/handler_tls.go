package server

import (
	"net/http"

	"mike/backend/httputil"
	"mike/backend/tls"
	"mike/pkg/logging"
)

// caCertFileName is the download name offered for the local CA. The frontend names the same file in
// CA_DOWNLOAD.fileName, and a download that arrives under a different name confuses the installer steps
// the login gate prints beside the link.
const caCertFileName = "mike-ca.crt"

// HandleTLSCA serves the local CA certificate that signed the server's default
// certificate, so another device can import it. Settings → About links here, and so does the login
// gate's notice for a browser whose connection will not run a passkey prompt — which is why the route
// is public: the device that needs the certificate is the one with no session.
// It is GET-only, and CAFile is the only path it can name — no request field is a path here.
func (s *Server) HandleTLSCA(w http.ResponseWriter, r *http.Request) {
	// A certificate the operator brought has no CA of ours to hand out.
	if !s.localCAvailable() {
		httputil.JSONError(w, http.StatusNotFound, "no CA certificate: the server is not using its default certificate")
		return
	}

	certPEM, err := tls.CertPEM(s.tlsConfig.CAFile)
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, "failed to read CA certificate")
		return
	}

	w.Header().Set("Content-Type", "application/x-x509-ca-cert")
	w.Header().Set("Content-Disposition", `attachment; filename="`+caCertFileName+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(certPEM); err != nil {
		logging.For(logging.HTTP).Warnf("TLS: failed to serve CA certificate: %v", err)
	}
}
