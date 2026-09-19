// Package updates answers one question — is there a newer MIKE release, and can this install take it —
// and owns every step from the manifest fetch to staging the archive into the install directory.
//
// The mechanism, the install shapes that refuse, and what is verified: docs/auto-update.md.
package updates

// Environment variable names this package owns. backend/startup holds the copy the ladder reads, and
// startup.TestEnvNamesMatchTheirOwners holds the two together.
const (
	// EnvManifestURL names the release manifest. It is read once, at startup.
	EnvManifestURL = "MIKE_UPDATE_URL"
	// EnvDisableUpdates is the organization-level kill switch: exactly "1" and this process will not
	// check for, download, install or restart onto a release.
	EnvDisableUpdates = "MIKE_DISABLE_UPDATES"
)

// DefaultManifestURL is the hosted release manifest, used when MIKE_UPDATE_URL names another. Assets are
// expected on the same host; docs/auto-update.md owns the grammar.
const DefaultManifestURL = "https://getmike.dev/version"
