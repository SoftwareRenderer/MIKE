/**
 * The one URL guard Settings → Extensions puts behind an `href`. A manifest's `license_url` and
 * `homepage` are text that arrived from the registry, and an `href` follows whatever scheme it
 * names, so an unsafe scheme renders no link at all rather than a link to `javascript:`.
 *
 * `frontend/html-sinks.py` re-reads the `u.protocol` marker from this module: the guard is a guard
 * only while it still checks the scheme.
 */
export function safeUrl(url: string): string {
    if (!url) return '#';
    try {
        const u = new URL(url);
        if (!['http:', 'https:'].includes(u.protocol)) return '#';
        return url;
    } catch {
        return '#';
    }
}
