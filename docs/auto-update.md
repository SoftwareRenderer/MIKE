# Server auto-update — mechanism

Use this guide before modifying `backend/updates/`, `scripts/gen-version-manifest.py`, the `update_check_enabled`
preference, or the `/api/updates/*` routes.

Update checks involve executing network-provided code, so the process is designed around multiple safety refusals:

- **Updates are purely opt-in.** Users must first opt-in to checks and then explicitly click *Install* to modify the installation directory.
- **A managed deployment does none of it.** A server whose release is decided elsewhere answers `managed` and refuses all four actions before anything reaches the network or the disk. Two things make a deployment managed: the `MIKE_DISABLE_UPDATES` switch, and an install that cannot replace its own files. The install shapes that cannot are a container image, where a swapped binary survives `docker restart` but is gone after the next `compose up`; a build whose version is not a release's; and a binary whose siblings do not match a release layout. The shape is decided once, by `classifyInstall`.
- **A managed server answers one sentence.** Settings → About disables the control and says *Updates are managed by your administrator.* It shows no verdict, no reason paragraph and no command. What a person with an image can do is run `docker compose pull`, and whether to do that is their operator's call rather than this screen's.

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

The `MIKE_DISABLE_UPDATES` environment variable overrides all other settings. If set to `1`, updates are completely disabled. `Status` answers `managed`, and the About tab says updates are managed by the administrator.

The other cause is the install shape, and it answers the same way: `managed` is the switch **or** `install.can_stage == false`, computed in one place so the client that hides the click and the refusal that backs it up cannot disagree. Which of the two made a process managed is logged once at boot, so the operator's log is the record.

Update prompts are managed by the client. If a user's preference is 'unanswered', the browser prompts them on boot. Dismissing the prompt without answering will cause it to reappear on the next boot. A managed server is never asked: no prompt, no daily check, no preference written on its behalf.

A refusal is a conflict carrying a sentence the browser may show. A managed install's refusal names who manages the release and repeats the shape's cause; `Install.Reason` holds that cause, and the status answer ships it as `install.reason` for whoever reads it, while the client renders the one sentence and nothing else. A release build is told from a source checkout by the presence of `prompts/*.tmpl` beside the binary.

Tests: `backend/updates/disable_test.go` (both causes, all four steps, zero requests) and `TestAManagedInstallRefusesEveryUpdateRoute` in `backend/server/handler_updates_test.go`.

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
| `POST /api/updates/check` | one manifest GET | `MIKE_DISABLE_UPDATES=1`, the preference is off, or the install is managed |
| `POST /api/updates/prepare` | the archive | `MIKE_DISABLE_UPDATES=1`, the install is managed, no newer release known, or an update already running |
| `POST /api/updates/apply` | none | `MIKE_DISABLE_UPDATES=1`, the install is managed, a task turn is running, or no archive downloaded |
| `POST /api/updates/restart` | none | `MIKE_DISABLE_UPDATES=1`, the install is managed, nothing staged, or no restart path |

All routes require a session cookie and CSRF protection. Bearer tokens are not permitted. `prepare` and `apply` run as detached operations to ensure they complete even if the browser tab is closed.

Check results are cached in `update_check_result` to avoid redundant network calls. The cache is invalidated if the server is already running the newest release.

## The client

`$lib/services/update-check.svelte.ts` manages the state. The UI displays a dot in the sidebar, top-bar, and About section: green for available updates, ringed for downloaded updates, and outlined for pending prompts.

The daily check is bound to the install, not the browser. One browser booting today triggers the manifest GET for all other browsers on that install. Downloads are stamped to prevent repeated reload rituals.

The SPA is embedded in the binary. Installing a new version updates the client, and the stale-asset path (`docs/frontend-updates.md`) handles the eventual browser reload.
