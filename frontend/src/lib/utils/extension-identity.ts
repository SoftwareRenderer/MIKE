/**
 * Extension identity: `<slugified-author>/<name>`. Manifests and the registry carry a display
 * author ("Microsoft"), the on-disk namespace is a slug ("microsoft"), so every comparison between
 * the installed, registry and local lists must use this key. Shared with the e2e helper so both
 * compute the same thing.
 */

export function slugifyAuthor(author: string | undefined): string {
    const slug = (author ?? '').trim().toLowerCase();
    if (!slug) return 'unknown';
    let out = '';
    let lastDash = false;
    for (const ch of slug) {
        const ok = (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch === '-' || ch === '_' || ch === '.';
        if (!ok) {
            if (lastDash) continue;
            out += '-';
            lastDash = true;
            continue;
        }
        out += ch;
        lastDash = false;
    }
    if (!out) return 'unknown';
    return out.replace(/[-._]+$/, '');
}

export function identityKey(author: string | undefined, name: string): string {
    return `${slugifyAuthor(author)}/${name}`;
}
