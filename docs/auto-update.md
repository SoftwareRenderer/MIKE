# Server auto-update — mechanism

Use this guide before modifying `backend/updates/`, `scripts/gen-version-manifest.py`, the `update_check_enabled`
preference, or the `/api/updates/*` routes.

Update checks involve executing network-provided code, so the process is designed around multiple safety refusals:

- **Updates are purely opt-in.** Users must first opt-in to checks and then explicitly click *Install* to modify the installation directory.
- **Containers are excluded.** Since changes to image layers are volatile, containers receive instructions to run `docker compose pull` instead of downloading files directly.
- **Non-release builds are excluded.** Development checkouts and builds whose siblings do not match a release layout do not self-modify.

## The manifest

`https://getmike.dev/download/version.json` answers one JSON object:

```json
{
  "schema": 1,
  "latest": "1.4.2",
  "published": "2026-09-18T08:53:20Z",
  "assets": {
    "linux/amd64": {
      "url": "https://getmike.dev/download/mike-server-1.4.2-linux-amd64.tar.gz",
      "sha256": "<64 hex>"
    },
    "linux/arm64": { "url": "…", "sha256": "…" }
  }
}
```

Manifest validation is strict to ensure binary integrity:

- `schema`: Must be a known value. Newer schemas are refused.
- `latest`: Must be a dotted version number. Non-standard versions (e.g., `dev`, commit hashes) are treated as unknown.
- URLs must use HTTPS and originate from the manifest's host. No redirects are followed.
- SHA-256 hashes must be 64 lowercase hex characters.
- Unknown fields are ignored.

`notes_url` and any future field are optional. A signature field can be added without breaking older clients.

## Publishing

`make package` writes `dist/version.json` and `dist/sha256sum.txt`. The generator reads digests from `sha256sum.txt` to ensure the manifest and the checksum file agree. It refuses versions without archives, invalid filenames, or non-HTTPS asset roots. `--check` verifies the disk against the generated output. Test: `scripts/gen-version-manifest_test.sh`.

### The release instant

`scripts/release-instant.sh` answers one epoch for a whole build, and `scripts/package-release.sh` stamps
every archive member with it and hands the same value to the manifest generator, so `published` and the
archive mtimes are one instant: when the release's commit landed.

The instant is the commit `v<version>` peels to, read as its **committer** date. A rebase or an amend moves
that date and leaves the author date alone, and an annotated tag's tagger date records when someone ran
`git tag -a`, which is not when the code changed.

`SOURCE_DATE_EPOCH` in the environment wins, which is how a reproducible-build harness states the instant it
wants; a value that is not epoch seconds stops the build and names the variable. A version nobody tagged
falls back to this machine's clock and says so on stderr — the only way to package a checkout with no tag,
and it costs the byte-comparability.
Test: `scripts/release-instant_test.sh`.

Upload order is critical:

1. Upload all archives named in `version.json` and verify their digests.
2. Upload `version.json` last, to the path the manifest URL names — `https://getmike.dev/download/version.json` by default, or wherever `MIKE_UPDATE_URL` points.
3. If a step fails, restore the previous `version.json`.

The default asset root is `https://getmike.dev/download` (configurable via `UPDATE_BASE_URL`), and the manifest is published in that same directory. The manifest URL is `updates.DefaultManifestURL` (configurable via `MIKE_UPDATE_URL`). The client compares the two hosts, never their directories, so a manifest beside the archives and an archive anywhere else on that host both pass.

## What is verified, and what is not

Verification steps:
1. The downloaded archive's hash is checked against the manifest before extraction.
2. The archive is test-extracted to a temporary directory to ensure it contains only regular files with valid paths and sizes.
3. The archive is re-hashed at install time to detect corruption or tampering.

The identity of the manifest publisher is not verified beyond TLS and the SHA-256 digest. Users can manually verify digests via a second channel. Future versions may include `minisign` for cryptographic verification.

## Who decided: the ladder

The `update_check_enabled` configuration has three states:

| Stored | Meaning |
|---|---|
| absent | Never asked. Checks are disabled, and the user will be prompted. |
| `false` | Declined. Checks are disabled. |
| `true` | Enabled. The server performs a daily manifest check. |

The `MIKE_DISABLE_UPDATES` environment variable overrides all other settings. If set to `1`, updates are completely disabled. `Status` answers `disabled`, and the About tab indicates that updates are managed by the organization.

Update prompts are managed by the client. If a user's preference is 'unanswered', the browser prompts them on boot. Dismissing the prompt without answering will cause it to reappear on the next boot.

Refusals return a message displayed in the About tab. Containers include the `docker compose pull` command. The system distinguishes release builds from source checkouts by checking for the presence of `prompts/*.tmpl` beside the binary.

## Prepare: fetch, verify, keep

The `Prepare` phase is automatic and guarded by a single operation slot to prevent concurrent updates. Process order:

1. Refuse if the install cannot take a release, no newer release is known, or an update is already staged/downloaded.
2. Download into `.update-prepared/release.tar.gz.part` within the install directory, hashing as it goes.
3. Compare the digest with the manifest. Mismatches delete the part file.
4. Test-unpack the archive into a temporary directory to verify file shapes and sizes.
5. Rename the part file to `release.tar.gz`, write `prepared.json`, and update the state.

Stale staging directories, partial downloads, or temporary verification trees are cleaned up during the next `New` operation.

## Apply: re-verify, swap, stop

The `Apply` phase is triggered by the user:

1. Re-hash `release.tar.gz` against the recorded digest to ensure no tampering occurred.
2. Unpack into a temporary directory inside the install directory. Only the files the server reads are
   kept: `mike-server`, `sandbox-bridge` and `prompts/*.tmpl`. The swap keeps the operator's text current;
   a template missing from the install renders from the copy compiled into the binary
   (`backend/prompts/layer.go`) rather than failing the turn.
3. Determine which files to swap based on the paths currently used by the server.
4. Backup the current release to `.update-backup/` and rename new files into place.
5. Record the staged version and backup path, then clear the prepared state.

Rolling back is a manual process using the backup directory.

A release ships the sample Dockerfiles under `examples/` and installs nothing there: the swap covers what
the running server opens, and nothing opens a sample. A person who edited one in place keeps the edit.

## Restart

`POST /api/updates/restart` triggers a server restart if a release is staged. It signals the main process to shut down gracefully and `exec` the new binary. The response is sent before the shutdown sequence begins to ensure the client receives confirmation.

A process with no restart path (e.g., started by hand) is refused.

## The five routes

| Route | Egress | Refuses when |
|---|---|---|
| `GET /api/updates/status` | none | never |
| `POST /api/updates/check` | one manifest GET | `MIKE_DISABLE_UPDATES=1` or preference is off |
| `POST /api/updates/prepare` | the archive | `MIKE_DISABLE_UPDATES=1`, no newer release known, or update already running |
| `POST /api/updates/apply` | none | `MIKE_DISABLE_UPDATES=1`, a task turn is running, or no archive downloaded |
| `POST /api/updates/restart` | none | `MIKE_DISABLE_UPDATES=1`, nothing staged, or no restart path |

All routes require a session cookie and CSRF protection. Bearer tokens are not permitted. `prepare` and `apply` run as detached operations to ensure they complete even if the browser tab is closed.

Check results are cached in `update_check_result` to avoid redundant network calls. The cache is invalidated if the server is already running the newest release.

## The client

`$lib/services/update-check.svelte.ts` manages the state. The UI displays a dot in the sidebar, top-bar, and About section: green for available updates, ringed for downloaded updates, and outlined for pending prompts.

The daily check is bound to the install, not the browser. One browser booting today triggers the manifest GET for all other browsers on that install. Downloads are stamped to prevent repeated reload rituals.

The SPA is embedded in the binary. Installing a new version updates the client, and the stale-asset path (`docs/frontend-updates.md`) handles the eventual browser reload.
