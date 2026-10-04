package server

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"mike/backend/auth"
	"mike/backend/startup"
	"mike/pkg/logging"
)

// Boot pairing is a fresh install's only way in: with no credential registered nothing can sign in, and
// the one thing that can change that is a code printed where the operator is already looking. The code
// buys a registration and nothing else (docs/passkeys.md), so printing it is not printing a password —
// once it is spent there is nothing left to steal from the log it was written to.

// pairAtBoot mints and prints the boot pairing code when this install holds no passkey. A paired install
// prints nothing: an unattended restart must not announce a live code into a log nobody is reading.
func (s *Server) pairAtBoot(ctx context.Context) {
	paired, err := s.passkeys.HasPasskeys(ctx)
	if err != nil {
		logging.For(logging.Auth).Errorf("could not check whether this install has a passkey, so no pairing code was printed: %v", err)
		return
	}
	if paired {
		return
	}
	code, err := s.pairing.Mint(auth.BootPairingTTL)
	if err != nil {
		logging.For(logging.Auth).Errorf("could not mint a pairing code, so this install cannot be paired: %v", err)
		return
	}
	origin := s.bootPairingOrigin()
	url := auth.PairingURL(origin, code.Code)
	// The code is written here and nowhere else, and the browser lane reads it back out of this text
	// (docs/testing.md → Passkeys in the e2e lane).
	logging.Banner(auth.PairingBanner(auth.FormatPairingCode(code.Code), url, code.ExpiresAt))

	if !startup.IsLoopbackAddr(s.settings.Listen) {
		logging.For(logging.Auth).Warnf("this server listens on %s, which other machines can reach: anyone who can reach that "+
			"port can spend %.0f minutes trying this code, at %d attempts per minute per address",
			s.settings.Listen, auth.BootPairingTTL.Minutes(), s.settings.LoginRateLimit)
	}
	if _, err := auth.NewRelyingHost(s.bootPairingAuthority(), true); err != nil {
		logging.For(logging.Auth).Warnf("the URL above points at %q, which cannot hold a passkey: %v. Open MIKE by a hostname — "+
			"a name in MIKE_ALLOWED_HOSTS — and type the code there", s.bootPairingHost(), err)
	}
}

// bootPairingHost is the hostname the printed URL points at: the first Host the operator pinned, because
// that is the name their browser will already be allowed to use, and otherwise the listen address with
// every spelling of "this machine" replaced by the one that can carry a credential. A wildcard or a bare
// address names no host a passkey can be bound to, and 127.0.0.1 is a secure context that no browser will
// accept as a relying-party id, so both resolve to localhost rather than to a URL that refuses the very
// ceremony it was printed for.
func (s *Server) bootPairingHost() string {
	for _, host := range s.settings.AllowedHosts {
		if host = strings.TrimSpace(host); host != "" {
			return host
		}
	}
	listen := strings.Trim(strings.TrimSpace(s.settings.Listen), "[]")
	if listen == "" || listen == "0.0.0.0" || listen == "::" || listen == "*" {
		return "localhost"
	}
	if ip := net.ParseIP(listen); ip != nil && ip.IsLoopback() {
		return "localhost"
	}
	return listen
}

// bootPairingAuthority is that host with the listen port on it, which is what a relying-party host is
// derived from.
func (s *Server) bootPairingAuthority() string {
	return net.JoinHostPort(s.bootPairingHost(), strconv.Itoa(s.settings.Port))
}

// bootPairingOrigin is the scheme-and-host half of the pairing URL. TLS has no switch, so the printed
// address is always https; port 443 is the scheme's own and is left off.
func (s *Server) bootPairingOrigin() string {
	if s.settings.Port == 0 || s.settings.Port == 443 {
		return fmt.Sprintf("https://%s", s.bootPairingHost())
	}
	return fmt.Sprintf("https://%s:%d", s.bootPairingHost(), s.settings.Port)
}
