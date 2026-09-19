# Server auto-update — mechanism

Read this before touching `backend/updates/`, `scripts/gen-version-manifest.py`, the `update_check_enabled`
preference, or the five `/api/updates/*` routes.

An update check is the one place where a network answer becomes code running as the server's user, so the
path is built out of refusals. Three of them are the design, not the fine print:

- **Nothing is applied without a click.** Opt-in, then *Install* are two decisions, and only the second one
  touches the install directory. What the opt-in buys is automatic and reversible by construction: fetch the
  manifest, verify the archive, put it in its own directory. Nothing replaces a file a running process uses.
- **A container refuses.** A binary swapped inside an image layer survives `docker restart` and vanishes on
  the next `compose up`; a half-applied update reads as a working one. A container gets nothing downloaded
  at all — the answer is the `docker compose pull` instruction and the reason.
- **A tree we cannot name refuses.** A development build and anything whose siblings do not look like a
  release directory do not self-modify.

## The manifest

`https://getmike.dev/version` answers one JSON object:

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

The rules are strict on purpose — a manifest that decodes "close enough" is a manifest that offers the wrong
binary:

- `schema` is a known value. A newer schema is refused, not partially read.
- `latest` is a dotted number. A version that is not one (`dev`, `docker`, `compose`, a commit hash) also
  makes the *client* answer "cannot tell", which is never rendered as "up to date" and never offers a
  downgrade.
- Every `url` is `https` **and** on the manifest's own host. `httputil.DoOutbound` follows no redirect, and
  the host is re-checked after the decode, so one decision about where this install may fetch covers both
  files.
- Every `sha256` is 64 lowercase hex.
- Unknown fields are ignored; an asset for another `os/arch` is ignored until it is asked for.

`notes_url` and any future field are optional. A signature field can be added without breaking an older
client's decode; nothing verifies one yet (see *What is verified*).

## Publishing

`make package` writes `dist/version.json` next to `dist/sha256sum.txt` (`scripts/package-release.sh` →
`scripts/gen-version-manifest.py`). The generator reads the digests **out of** `sha256sum.txt` and never
re-hashes an archive, which is what keeps the two files a buyer and an install compare in agreement. It
refuses: a version no archive carries, a line whose name is not a release name (a stray file is an error,
not a skipped row), a digest that is not 64 hex, an asset root that is not `https`, and an empty checksum
file. `--check` verifies the file on disk against what it would write, ignoring `published` — the
announcement instant is the only field not derived from the archives. `SOURCE_DATE_EPOCH` fixes it for a
reproducible build. Test: `scripts/gen-version-manifest_test.sh`.

Upload order matters, because the manifest is the announcement:

1. Upload every archive named in `version.json`, and check each digest against `sha256sum.txt` at its URL.
2. Upload `version.json` last.
3. If a step fails, put the previous `version.json` back — an earlier manifest describes a release that is
   still fully downloadable.

The asset root is `https://getmike.dev/download` unless `UPDATE_BASE_URL` says otherwise at package time,
and the manifest URL is the server's own default (`updates.DefaultManifestURL`) unless `MIKE_UPDATE_URL`
names another. The two share a host by convention; the client enforces it by comparing them.

## What is verified, and what is not

Verified: the archive's bytes hash to the digest the manifest named, before anything is extracted; the
archive is unpacked once into a throwaway tree at prepare time, so a tarball that would be refused at
install is refused before anyone is promised they can install it; **the archive on disk is hashed again at
install time** against the digest recorded when it was fetched, so an edited or truncated file is refused
rather than installed; every tar member is a regular file under the expected name, with no absolute path, no
`..`, no symlink or hardlink, inside the member-count and size caps.

The install-time hash is against the recorded digest, not a second network call: the number it compares with
is the one the manifest gave for that version.

Not verified: that the host serving the manifest is the host that published it. TLS plus a digest proves the
download matches what the same server's manifest said, and nothing more — the same caveat the package README
gives about `sha256sum.txt`. An operator who wants a second channel verifies the digest themselves; a
`minisign` key compiled into the binary would close it and is the intended next step.

## Who decided: the ladder

`update_check_enabled` (one row in the `config` table) is the operator's answer, and it has three states,
because "not yet asked" is not the same answer as "no":

| Stored | Meaning |
|---|---|
| absent | Never asked. Checks are off **and the browser still owes the question**. |
| `false` | Declined. Off, and nothing asks again. |
| `true` | On: one small GET per day, the download of a release that GET names, and the dot. |

`MIKE_DISABLE_UPDATES` outranks all three for every browser of that deployment. Exactly `1` and this process
will not check for, download, install or restart onto a release; `0` or unset leaves the decision to the
stored preference; any other value warns at boot that it **disabled nothing** (`backend/startup →
resolveDisable`). It is a refusal, not a UI state: `check`, `prepare`, `apply` and `restart` each refuse at
the service, so neither a cached "a newer release exists" row nor an archive some earlier process already
fetched can get past it, and the refusal names the variable. `Status` answers `disabled`, which is what the
client stops on — no dot, no first-run question, no install prompt — and About shows
`Updates are managed by your organization` in place of everything clickable. It replaces
`MIKE_UPDATE_CHECK`, which is read by nothing and only warns an `.env` still carrying it.

Nothing outside `backend/startup` reads the variable. The preference arrives per request as
`updates.Settings{Enabled, Answered}`; the switch is a fact the service is constructed with, so no caller
can forget to apply it. One `Info` per boot names which posture the process came up on.

The first-run question is the client's, not the server's: `update-check.svelte.ts` asks once per browser
when the status says `answered: false` and nothing forbids it, stamping the ask in IndexedDB so a write that
fails does not turn the prompt into a boot ritual. **No** and **Yes** both write the preference — the
question is answered either way, and a decline must be kept for the same reason an acceptance is. The
catalog text is dictated (`updates.consent.*`); what turning it on also downloads is said in `About`, where
the switch lives, rather than in the dialog.

## What this install is

`classifyInstall` answers once, at construction, from facts that are all arguments (`install.go`), so a test
can ask about a tree it is not standing in:

| Kind | Decided by | May stage |
|---|---|---|
| `packaged` | Version is a dotted number **and** the resolved `os.Executable()` sits beside `prompts/*.tmpl` | yes |
| `container` | `/.dockerenv` or `/run/.containerenv` exists | no |
| `source` | Version is not a dotted number (`dev`, `docker`, `compose`, a commit hash) | no |
| `unknown` | Anything else — a distro package, a hand-assembled layout | no |

Every refusal carries the sentence the About tab shows, and a container's carries the `docker compose pull`
command beside it. The two-version rule is deliberate: a source checkout has `prompts/*.tmpl` beside its
binary too, so the layout alone would call a development tree a release.

## Prepare: fetch, verify, keep

`Prepare` is the automatic half. One operation slot guards the whole process (`beginWork`; there is one
install directory, so nothing is keyed), and the name it records is what `Status.working` reports and what a
second request is refused with. It runs in this order and nothing else:

1. Refuse unless this install may take a release, a newer release with an archive for this machine is known,
   nothing is staged, and nothing is already downloaded. Never a second archive over the first: one at a
   time, and the prompt a person may be reading belongs to the release already waiting.
2. Download into `.update-prepared/release.tar.gz.part` — a name nothing reads — **inside the install
   directory**, so the install click is a rename on one filesystem, streaming under `MaxPackageBytes` and
   hashing as it goes.
3. Compare that digest with the manifest's. A mismatch removes the part file and moves nothing.
4. Unpack it once into `.update-prepared/verify-*` and throw the tree away, reporting how many files
   installing would replace. An archive that would be refused at install is refused here, before anyone is
   promised they can install it.
5. Rename the part file to `release.tar.gz`, write `prepared.json` (version, sha256, bytes, how many files
   installing would replace, when), record `prepared_*` in the state row and **return**.

Nothing outside `<installDir>/.update-prepared/` is written by any of it, which is what makes running it
without asking safe.

## Apply: re-verify, swap, stop

`Apply` is the click that changes anything:

1. Refuse with nothing prepared, then hash `release.tar.gz` again against the digest recorded when it was
   fetched. A file that no longer matches clears the prepared state and refuses naming the version, so the
   answer is "check again" rather than an install of unknown bytes.
2. Unpack into a fresh `0700` `MkdirTemp` inside the install directory, member by member: regular files
   only, no absolute path, no `..`, no symlink or hardlink, at most `maxArchiveMembers` members, each under
   the same cap. Only the names the packager writes survive (`mike-server`, `sandbox-bridge`,
   `prompts/*.tmpl`, `sandbox/*.Dockerfile`).
3. Decide the swap from the paths *this* server uses — its own executable, the bridge path it resolves — not
   the names the archive happens to carry.
4. Copy what is about to be replaced into `<installDir>/.update-backup/` — one backup, the previous release
   — then `rename` each new file into place.
5. Record `staged_version` and `backup_path`, clear the prepared state, delete the archive and **return**.
   The handler then asks for the restart: the click that installs is the click that restarts, and
   `restarting: false` is a success that says the swap landed and the process must be restarted by hand.

Everything before the first `rename` is revertible by deleting the staging directory; after it, the backup
directory is the rollback and the answer names it. Rolling back stays manual on purpose: a boot-and-verify
loop would need a second data directory, port and TLS identity.

A leftover `.update-staging-*`, a `*.part` still being written or a `verify-*` nobody threw away is reaped by
the next `New`, by name, and by nothing else. A **prepared** archive survives the process that downloaded it,
but only while the stored row names its version: a row that names another release, a record that no longer
decodes or an archive that has gone loses the directory. The boot settle that clears a `staged_version` this
process is now running clears a `prepared_version` it is now running by the same rule.

## Restart

`POST /api/updates/restart` refuses unless something is staged, then calls the `Restart` func `main.go` owns.
That func writes to the same channel the SIGINT/SIGTERM select reads, so the shutdown is the declared stage
list rather than a second one invented here; `main.go` re-`exec`s the same path with the same argv and cwd
after `RunShutdownWithDeadline`, and exits non-zero if the `exec` fails so the supervisor starts the binary
that is now on disk. The response is written first: the handler only wakes that select and returns, and the
shutdown waits for in-flight requests, which is why nothing there sleeps to let the body out.

A process with no restart path — a server started by hand with nothing watching it — is refused by name
before anything is promised, and the About tab shows that sentence.

## The five routes

| Route | Egress | Refuses when |
|---|---|---|
| `GET /api/updates/status` | none | never — it is how the client learns updates are disabled |
| `POST /api/updates/check` | one manifest GET | `MIKE_DISABLE_UPDATES` is on (names it); the preference is off |
| `POST /api/updates/prepare` | the archive | `MIKE_DISABLE_UPDATES` is on, this install cannot take a release, nothing newer is known, something is staged or already downloaded, an update operation is running |
| `POST /api/updates/apply` | none | `MIKE_DISABLE_UPDATES` is on, a task turn is running (names the tasks), nothing is downloaded, this install cannot take a release, the archive no longer matches its digest, an update operation is running |
| `POST /api/updates/restart` | none | `MIKE_DISABLE_UPDATES` is on, nothing is staged, an update operation is running, there is no restart path |

All five declare their verb in the pattern, none is in `tokenScope` (a bearer token is what a headless client
carries, and no headless client needs to replace this server's binary), and all five are session + CSRF.
`check` answers its refusal with a 409 whose sentence the About tab shows; `status` is what the client
re-reads after one, because the last good answer is still true. `prepare` and `apply` run on
`Server.Detached()` — a tab closing halfway through a 20 MB download must not decide whether this install
ever gets the release, and a shutdown must still be able to stop the copy phase before the renames
(`sideeffects.go`). The in-flight-task refusal sits on `apply` alone: fetching renames nothing.

The last check's result is persisted in the `update_check_result` config row so the dot is correct on the
next page load without a second outbound call — a cache, so a row that no longer decodes is reported and
dropped rather than refusing the boot. The row is also settled against the version that boots: if this
process is already running the release the row called newest, it stops saying an update is available, and a
staged or downloaded release it is now running stops asking for the click. Without that the green dot and the
install button would survive onto the release already applied, because the next check is at most once a day.

## The client

`$lib/services/update-check.svelte.ts` owns the state; `$lib/components/UpdateDot.svelte` renders it and
`Settings → About → UpdatesSection` is the only place it is explained. One pass per app open reads the status
(a local read, no egress), asks the first-run question if it is owed, then runs the outbound check at most
once a day and prepares whatever that check found. Both are stamped *before* the request, like
`app-update.ts`: the check stamps the day, and the download stamps the **release and** the day, so a fetch
that failed for no space on the device is retried once a day rather than once a reload, while a newer release
appearing today is still fetched today. There is no timer and no loop here. A download that the automatic
pass skipped is one button away in About, which is why the refusal a permanent failure produces is shown
rather than swallowed.

The dot is on every Settings control (sidebar row, top-bar icon, the empty state's gear): green for a
release with an archive for this machine, ringed when a release is downloaded and waiting for the install
click, outlined for the opt-in answer still owed. Clicking any of them opens Settings **on About**, because a
mark on a control should lead to the row that explains it. `container` and `source` installs get the reason
and the command where the buttons would be.

A finished download is announced once, in the browser that was open when it landed: *Install now* runs the
apply, *Later* leaves the release waiting and the dot and the About row carry it. The prompt is a
convenience, not the gate — dismissing it is a normal answer, not a deferral that expires, and the same
install is reachable from About afterwards.

Neither the check, the download nor the install reloads the page: the SPA is `go:embed`-ed into
`mike-server`, so installing the binary installed the client, and the stale-asset path
(`docs/frontend-updates.md`) is what eventually reloads the browser onto it.
