package server

import (
	"context"
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
	hosts := s.bootPairingHosts()
	urls := make([]string, 0, len(hosts))
	for _, host := range hosts {
		urls = append(urls, auth.PairingURL(host.origin, code.Code, ""))
	}
	// The code is written here and nowhere else, and the browser lane reads it back out of this text
	// (docs/testing.md → Passkeys in the e2e lane).
	logging.Banner(auth.PairingBanner(auth.FormatPairingCode(code.Code), urls, code.ExpiresAt))

	if !startup.IsLoopbackAddr(s.settings.Listen) {
		logging.For(logging.Auth).Warnf("this server listens on %s, which other machines can reach: anyone who can reach that "+
			"port can spend %.0f minutes trying this code, at %d attempts per minute per address",
			s.settings.Listen, auth.BootPairingTTL.Minutes(), s.settings.LoginRateLimit)
	}
	for _, host := range hosts {
		if _, err := auth.NewRelyingHost(host.authority, true); err != nil {
			logging.For(logging.Auth).Warnf("the URL above points at %q, which cannot hold a passkey: %v. Open MIKE by a hostname — "+
				"a name in MIKE_ALLOWED_HOSTS — and type the code there", host.origin, err)
		}
	}
}

// bootPairingHost is one address the pairing banner prints a URL for. The origin is spelled exactly as the
// Host pinning accepts it and as NewRelyingHost spells a ceremony origin, so a person who opens the printed
// URL can pair, and the verifier releases the passkey they made there.
type bootPairingHost struct {
	origin    string
	authority string
}

// bootPairingHosts are the addresses the banner offers a pairing URL for, because a passkey belongs to the
// address it was paired at. With no authorized host the listen address stands in, and a wildcard, bare
// address or loopback address becomes localhost, because a browser will not accept one as a relying party.
func (s *Server) bootPairingHosts() []bootPairingHost {
	var hosts []bootPairingHost
	seen := make(map[string]struct{}, len(s.settings.AllowedHosts))
	for _, entry := range s.settings.AllowedHosts {
		host := normalizeHost(entry)
		if _, duplicate := seen[host]; host == "" || duplicate {
			continue
		}
		seen[host] = struct{}{}
		hosts = append(hosts, newBootPairingHost(host, s.settings.Port))
	}
	if len(hosts) > 0 {
		return hosts
	}
	return []bootPairingHost{newBootPairingHost(s.bootPairingListenHost(), s.settings.Port)}
}

func (s *Server) bootPairingListenHost() string {
	listen := strings.Trim(strings.TrimSpace(s.settings.Listen), "[]")
	if listen == "" || listen == "0.0.0.0" || listen == "::" || listen == "*" {
		return "localhost"
	}
	if ip := net.ParseIP(listen); ip != nil && ip.IsLoopback() {
		return "localhost"
	}
	return listen
}

func newBootPairingHost(host string, port int) bootPairingHost {
	portText := strconv.Itoa(port)
	origin := "https://" + host
	if port != 0 && port != 443 {
		origin += ":" + portText
	}
	return bootPairingHost{origin: origin, authority: net.JoinHostPort(host, portText)}
}
