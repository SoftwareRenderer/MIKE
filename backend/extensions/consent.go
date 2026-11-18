package extensions

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"

	"mike/backend/types"
)

// A development row's manifest is written by an agent, so the server asks a load the same question an
// install asks a person: a client that skipped the dialog must not turn on egress by itself
// (docs/extension-authoring.md → Loading). consent_test.go and
// frontend/src/lib/utils/__tests__/extension-consent-gate.unit.ts run the same fixtures through both ends
// of the rule.

// dangerousUIPermissions mirrors DANGEROUS_PERMISSIONS in
// frontend/src/lib/utils/extension-permissions.ts. Both lists must name the same permissions.
var dangerousUIPermissions = map[string]bool{
	"network.proxy":        true,
	"preview.serveScripts": true,
	"preview.serveNetwork": true,
}

// consentAsks names what a manifest asks for beyond its own files: network access, a permission that
// leaves the sandbox, or a folder on this host. consentRequired and the refusal both read this one list.
func consentAsks(caps *Capabilities) []string {
	if caps == nil {
		return nil
	}
	var out []string
	if caps.Network != nil {
		if hosts := slices.Sorted(slices.Values(caps.Network.AllowedDomains)); len(hosts) > 0 {
			out = append(out, "network access to "+strings.Join(hosts, ", "))
		} else {
			out = append(out, "the network capability")
		}
	}
	var perms []string
	if caps.UI != nil {
		perms = caps.UI.Permissions
	}
	for _, p := range slices.Sorted(slices.Values(perms)) {
		if dangerousUIPermissions[p] {
			out = append(out, p)
		}
	}
	if caps.LSP != nil && caps.LSP.Sandbox != nil && caps.LSP.Sandbox.Dependency != nil {
		out = append(out, "a directory of this machine for its dependencies")
	}
	if caps.Agent != nil && caps.Agent.Sandbox != nil && len(caps.Agent.Sandbox.Mounts) > 0 {
		out = append(out, "a directory of this machine for the agent")
	}
	return out
}

// consentRequired is true when consentAsks is not empty. The paired tests named at the top of this file
// keep it equal to requiresInstallReview() on the frontend.
func consentRequired(caps *Capabilities) bool {
	return len(consentAsks(caps)) > 0
}

// consentFingerprint hashes the fields consentAsks reads, so an acknowledgement covers what the person
// was shown. A condition added to consentAsks must be added here too.
func consentFingerprint(caps *Capabilities) string {
	var b strings.Builder
	if caps != nil {
		if n := caps.Network; n != nil {
			b.WriteString("network\n")
			for _, d := range slices.Sorted(slices.Values(n.AllowedDomains)) {
				fmt.Fprintf(&b, "domain %s\n", d)
			}
			for _, k := range slices.Sorted(maps.Keys(n.Headers)) {
				fmt.Fprintf(&b, "header %s=%s\n", k, n.Headers[k])
			}
		}
		if u := caps.UI; u != nil {
			b.WriteString("ui\n")
			for _, p := range slices.Sorted(slices.Values(u.Permissions)) {
				fmt.Fprintf(&b, "permission %s\n", p)
			}
		}
		if lsp := caps.LSP; lsp != nil && lsp.Sandbox != nil {
			if d := lsp.Sandbox.Dependency; d != nil {
				fmt.Fprintf(&b, "dependency %s=%s\n", d.Env, d.ContainerPath)
			}
		}
		if a := caps.Agent; a != nil && a.Sandbox != nil {
			mounts := slices.Clone(a.Sandbox.Mounts)
			slices.SortFunc(mounts, func(x, y types.SandboxMount) int {
				if c := strings.Compare(x.HostPath, y.HostPath); c != 0 {
					return c
				}
				if c := strings.Compare(x.ContainerPath, y.ContainerPath); c != 0 {
					return c
				}
				return strings.Compare(x.Mode, y.Mode)
			})
			for _, m := range mounts {
				fmt.Fprintf(&b, "mount %s|%s|%s\n", m.HostPath, m.ContainerPath, m.Mode)
			}
		}
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:16])
}

// consentRefusal returns the fingerprint to store beside the loaded version, or "" when the manifest
// asks for nothing. It returns an *ErrConsentRequired when neither the row's stored value nor ack equals
// that fingerprint.
func consentRefusal(ext *Extension, manifest *ExtensionManifest, ack string) (string, error) {
	if !consentRequired(manifest.Capabilities) {
		return "", nil
	}
	fingerprint := consentFingerprint(manifest.Capabilities)
	if ext.NetworkAck == fingerprint || (ack != "" && ack == fingerprint) {
		return fingerprint, nil
	}
	return "", &ErrConsentRequired{Name: ext.Name, Manifest: manifest, Fingerprint: fingerprint}
}

// ErrConsentRequired is a load refused because nobody has acknowledged what the version asks for. The
// version already running is left in place.
type ErrConsentRequired struct {
	Name        string
	Manifest    *ExtensionManifest
	Fingerprint string
}

func (e *ErrConsentRequired) Error() string {
	return fmt.Sprintf("this version asks for access you have not approved for it: %s", strings.Join(e.Reasons(), ", "))
}

// Reasons is the refusal in words, for a client that cannot show the dialog.
func (e *ErrConsentRequired) Reasons() []string {
	asks := consentAsks(e.Manifest.Capabilities)
	if len(asks) == 0 {
		return []string{"access it has not declared"}
	}
	return asks
}
