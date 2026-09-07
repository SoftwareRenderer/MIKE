/**
 * The rule MIKE applies to a clone URL before it is allowed to go anywhere that
 * outlives the tab.
 *
 * `repoNameFromGitUrl` (`utils/repo-name`) derives the display name; this decides
 * whether a URL is safe to hand to the offline queue. Two rules, and both have to read
 * the same three URL shapes — `https://host/org/repo`, the scp-like `git@host:org/repo`
 * with no scheme at all, and `ssh://git@host:22/org/repo` — so a change to what counts
 * as a shape here must be read against that file too: getting it wrong in one direction
 * breaks a clone, in the other it leaks a token.
 */

/**
 * Whether the URL embeds a credential in its userinfo.
 *
 * What this is guarding: the offline queue is persisted to IndexedDB, so a URL
 * queued while offline is still on disk after the tab closes and the session
 * ends. `https://user:ghp_…@github.com/org/repo.git` is the ordinary way to clone
 * a private repository from this form, and the password in it is a personal access
 * token. The same reasoning that refuses to queue a provider API key
 * (`api/config.ts`) refuses to queue this.
 *
 * What it deliberately does *not* flag, because both are the normal way to reach a
 * private repo and neither carries a secret in the URL:
 *
 *   - `git@github.com:org/repo.git` — no scheme, and `git` is ssh's user, not a password
 *   - `ssh://git@host:22/org/repo.git` — same user, spelled as a URL
 *
 * A non-http scheme that carries a **password** (`ssh://user:pw@host`) is still a
 * password and is flagged.
 */
export function gitUrlCarriesCredentials(gitUrl: string): boolean {
    if (!gitUrl) return false;
    // scheme://userinfo@host — the scp-like form has no `://`, so it never matches.
    const match = /^([A-Za-z][A-Za-z0-9+.-]*):\/\/([^/?#]*)@/.exec(gitUrl.trim());
    if (!match) return false;
    const [, scheme, userinfo] = match;
    if (!userinfo) return false; // `https://@host/…` — an empty userinfo is not a credential
    // On http(s) the userinfo *is* the credential: a username alone is how a token
    // is pasted (`https:<token>@github.com`), so there is no benign shape to spare.
    const transport = scheme.toLowerCase();
    if (transport === 'http' || transport === 'https') return true;
    return userinfo.includes(':');
}
