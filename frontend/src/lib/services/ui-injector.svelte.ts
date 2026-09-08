import type { Extension } from '$lib/types';
import { computeGrants, CAP_THEME_UPDATE } from '$lib/extensions/capabilities';
import { readThemeVars } from '$lib/extensions/handlers/theme';
import { ExtensionHostChannel, mintChannelToken } from '$lib/extensions/channel';
import { dropExtensionAssetCache, extensionSetFingerprint } from './sw-cache';
import {
    INJECTION_POINTS,
    type InjectionPoint,
    type InjectionContext,
} from '$lib/extensions/types';

export { INJECTION_POINTS };
export type { InjectionPoint, InjectionContext };

export const THEME_CSS_KEY = 'kanban-theme-css';
const THEME_LINK_ID = 'kanban-theme-stylesheet';
// The static prefix every extension asset is served under.
const EXTENSIONS_PREFIX = '/extensions/';

// authorDir turns a registry author into a safe, single-segment directory name
// (mirrors backend authorDir). Installed extensions live at
// ./extensions/<author>/<name>, served as /extensions/<author>/<name>/.
function authorDir(author?: string): string {
    let slug = (author ?? '').toLowerCase().trim();
    if (!slug) return 'unknown';
    let out = '';
    let lastDash = false;
    for (const ch of slug) {
        const ok =
            (ch >= 'a' && ch <= 'z') ||
            (ch >= '0' && ch <= '9') ||
            ch === '-' ||
            ch === '_' ||
            ch === '.';
        if (!ok) {
            if (lastDash) continue;
            out += '-';
            lastDash = true;
            continue;
        }
        out += ch;
        lastDash = false;
    }
    return (out.trim().replace(/[-._]+$/, '') || 'unknown');
}

// extBaseURL returns the served base URL for an installed extension directory.
//
// The server's `served_url` wins when present: it is derived from the installed record, the
// same path the server reads, deletes and repairs the extension through. The author/name
// derivation below is a fallback guess, right only when the install landed under
// <slug(manifest.author)>/<name>. `ext.name` is percent-encoded so a name cannot reshape the
// same-origin path; the Go static handler decodes it again.
export function extBaseURL(ext: Extension): string {
    return (
        servedBaseURL(ext.served_url) ??
        `${EXTENSIONS_PREFIX}${authorDir(ext.manifest.author)}/${encodeURIComponent(ext.name)}`
    );
}

/**
 * The served directory the server reported, or null to fall back to the guess.
 *
 * Still validated rather than trusted: the value comes off the wire, and a base
 * that leaves `/extensions/` is a request for something else. Each segment is
 * decoded, refused when it is empty, a dot, a traversal, or carries a separator
 * or control character, then re-encoded — so a reported path can neither climb
 * out of the installed-extension tree nor smuggle an origin into a URL the app
 * will later hand to `fetch`.
 */
function servedBaseURL(served: unknown): string | null {
    if (typeof served !== 'string' || !served.startsWith(EXTENSIONS_PREFIX)) return null;
    const parts = served.slice(EXTENSIONS_PREFIX.length).split('/');
    if (parts.length > 2) return null; // deeper than <author>/<name>: not an extension directory
    const encoded: string[] = [];
    for (const part of parts) {
        let segment: string;
        try {
            segment = decodeURIComponent(part);
        } catch {
            return null; // a broken escape sequence is not a path
        }
        if (segment === '' || segment === '.' || segment === '..') return null;
        if (/[\\/\u0000-\u001f\u007f]/.test(segment)) return null;
        encoded.push(encodeURIComponent(segment));
    }
    return EXTENSIONS_PREFIX + encoded.join('/');
}

/** Resolve `.` and `..` in an absolute path, or return '' when it climbs above the root. */
function normalizePath(path: string): string {
    const segments: string[] = [];
    for (const segment of path.split('/')) {
        if (segment === '' || segment === '.') continue;
        if (segment === '..') {
            if (segments.length === 0) return ''; // above the root: not a path we can load
            segments.pop();
            continue;
        }
        segments.push(segment);
    }
    return '/' + segments.join('/');
}

/**
 * True when `path` names a file inside the installed-extension tree.
 *
 * Checked twice over, on the string and on its percent-decoded form: the Go
 * static handler decodes once before it routes, so `%2e%2e` and `%2F` are the
 * same segments to it as to the browser's URL parser. A query, a fragment, a
 * backslash or a control character is a refusal as well — an extension's asset
 * is a file it shipped, not a request.
 */
function isInsideExtensionTree(path: string): boolean {
    if (!path.startsWith(EXTENSIONS_PREFIX) || /[\\?#\u0000-\u001f\u007f]/.test(path)) return false;
    let decoded: string;
    try {
        decoded = decodeURIComponent(path);
    } catch {
        return false; // a broken escape sequence is not a path
    }
    for (const candidate of [path, decoded]) {
        if (!candidate.startsWith(EXTENSIONS_PREFIX)) return false;
        const segments = candidate.slice(EXTENSIONS_PREFIX.length).split('/');
        if (segments.length < 2) return false; // the tree root and an extension directory are not files
        if (segments.some((s) => s === '' || s === '.' || s === '..')) return false;
    }
    return true;
}

// resolveAssetURL resolves a manifest asset path (e.g. a css_url) against the
// extension's served base (extBaseURL), so a relative path like "./styles.css" or
// "styles.css" resolves to that directory plus the asset —
// /extensions/<author>/<name>/styles.css for a registry install, /extensions/<name>/…
// for one placed flat under the extensions root.
// The answer is that directory plus the asset, or '' — there is no third answer.
// '' covers a URL with a scheme (http:, https:, data:, …) or protocol-relative
// (`//host/…`), which a CSS extension must never load from an external origin, and
// equally a path that leaves that directory once `..` is resolved: the browser
// normalizes the URL before it sends it, so `/extensions/a/b/../../../api/config`
// *is* a request for `/api/config` with this session's cookie, whatever the string
// looked like on the way in. Containment is therefore decided on the normalized
// path, against the base's own normalized form — which also refuses a base that is
// not a directory. Mirrors validatePackRelPath/packFilePath on the Go side, which
// refuses the same shapes before a manifest is stored.
// A leading '/' is relative to the extension's own directory (never to the server root).
// Exported for tests.
export function resolveAssetURL(ext: Extension, asset: string): string {
    if (!asset) return asset;
    if (/^[a-z][a-z0-9+.-]*:/i.test(asset) || asset.startsWith('//')) return ''; // external — reject
    const base = normalizePath(extBaseURL(ext));
    if (!base) return ''; // a base that is not a directory has no assets to resolve against
    const resolved = normalizePath(`${base}/${asset.replace(/^\.?\//, '')}`);
    if (!resolved.startsWith(`${base}/`) || !isInsideExtensionTree(resolved)) return '';
    return resolved;
}

/**
 * Whether a stored theme URL may be pre-painted as a stylesheet: a same-origin
 * path inside the extension tree, the shape resolveAssetURL produces. The inline
 * script in index.html repeats this test by hand (it runs before any module can
 * be imported) — change both.
 */
export function isPrepaintableThemeURL(url: string): boolean {
    return isInsideExtensionTree(url);
}

export interface InjectionRegistration {
    extensionId: string;
    extensionName: string;
    point: InjectionPoint;
    moduleUrl: string;
    order: number;
    config: any;
    granted: string[];
}

export class UIInjector {
    registrations = $state<InjectionRegistration[]>([]);
    // Channels are scoped per anchor element. A point (e.g. `fileEditor`) can
    // be mounted in many instances at once (one open file each), and each
    // instance's anchor gets its own independent channel.
    // Key: anchor element -> ( `${extensionId}::${point}` -> channel ).
    private channels = new Map<HTMLElement, Map<string, ExtensionHostChannel>>();
    private version = $state(0);
    private activeCSSUrl = $state<string | null>(null);
    /** The installed set this registration was built from, and the drop its change started. */
    private extensionsFingerprint = '';
    private hasBaseline = false;
    assetCacheDropped: Promise<void> = Promise.resolve();

    registerFromExtensions(extensions: Extension[]) {
        this.destroyAll();

        // An updated or deleted extension leaves its previous bytes in the service
        // worker's cache, and that cache is the offline fallback for the frame the
        // host arms. Drop it when the installed set moved since this browser last
        // looked — not on the first list of a session, where there is nothing to
        // compare against and deleting would cost the offline fallback for free.
        const fingerprint = extensionSetFingerprint(extensions);
        if (this.hasBaseline && fingerprint !== this.extensionsFingerprint) {
            this.assetCacheDropped = dropExtensionAssetCache();
        }
        this.extensionsFingerprint = fingerprint;
        this.hasBaseline = true;

        this.registrations = [];
        let cssUrl: string | null = null;

        for (const ext of extensions) {
            if (!ext.enabled) continue;

            // CSS-type extensions: inject global stylesheet
            if (ext.type === 'css' && ext.manifest.capabilities?.css?.css_url) {
                if (!cssUrl) {
                    cssUrl = resolveAssetURL(ext, ext.manifest.capabilities.css.css_url);
                    if (!cssUrl) {
                        // Reported, not swallowed: a pack whose stylesheet cannot be resolved
                        // otherwise just never paints, which reads as a broken theme.
                        console.error(
                            `[ui-injector] ${ext.name}: css_url ${JSON.stringify(ext.manifest.capabilities.css.css_url)} is not a file in the extension's own directory; no theme stylesheet loaded.`,
                        );
                    }
                }
                continue;
            }

            // UI-type extensions: register injection points. The entry page is
            // always index.html in the extension's own directory.
            if (ext.type !== 'ui' || !ext.manifest.capabilities?.ui) continue;
            const ui = ext.manifest.capabilities.ui;
            if (!ui.injection_points) continue;
            const moduleUrl = `${extBaseURL(ext)}/index.html`;

            // Least privilege: grant only the manifest's requested capabilities
            // that exist in the catalog.
            const granted = computeGrants(ui.permissions);

            for (let i = 0; i < ui.injection_points.length; i++) {
                this.registrations.push({
                    extensionId: ext.id,
                    extensionName: ext.name,
                    point: ui.injection_points[i] as InjectionPoint,
                    moduleUrl,
                    order: ext.manifest.priority ?? 0,
                    config: ext.config,
                    granted,
                });
            }
        }

        // Apply or remove global theme stylesheet
        this.setThemeCSS(cssUrl);

        // Bump version so $effect watchers re-run
        this.version = this.version + 1;
    }

    private setThemeCSS(url: string | null) {
        const existing = document.getElementById(THEME_LINK_ID);
        if (url === this.activeCSSUrl && (!existing || url)) return;

        if (existing) {
            existing.remove();
        }

        if (!url) {
            this.activeCSSUrl = null;
            try { localStorage.removeItem(THEME_CSS_KEY); } catch {}
            return;
        }

        const link = document.createElement('link');
        link.rel = 'stylesheet';
        link.href = url;
        link.id = THEME_LINK_ID;
        // The theme variables only resolve after the stylesheet loads. Extensions
        // pull them via theme.get() on init, which can race the sheet load, so
        // push them once it's applied to settle mounted extensions on the final
        // palette. (No immediate broadcast here: registerFromExtensions/clearCache
        // destroy all channels before calling setThemeCSS, so none exist yet.)
        link.addEventListener('load', () => this.broadcastTheme());
        document.head.appendChild(link);

        // Cache it for the pre-paint step, and clear the hint when we would not
        // load it ourselves: index.html trusts whatever is in there.
        try {
            if (isPrepaintableThemeURL(url)) localStorage.setItem(THEME_CSS_KEY, url);
            else localStorage.removeItem(THEME_CSS_KEY);
        } catch {}

        this.activeCSSUrl = url;
    }

    /**
     * Push the current host theme variables to every mounted extension channel
     * so extension-rendered UI (e.g. the WYSIWYG markdown preview) follows the
     * active theme without the sandboxed iframe needing to inherit the host
     * stylesheet.
     */
    private broadcastTheme(): void {
        const variables = readThemeVars();
        for (const ac of this.channels.values()) {
            for (const ch of ac.values()) {
                ch.forward(CAP_THEME_UPDATE, { variables });
            }
        }
    }

    getRegistrations(point: InjectionPoint): InjectionRegistration[] {
        return this.registrations
            .filter(r => r.point === point)
            .sort((a, b) => a.order - b.order);
    }

    renderSlot(point: InjectionPoint, context: InjectionContext, anchor: HTMLElement | null = null): void {
        const regs = this.getRegistrations(point);
        if (regs.length === 0) return;

        if (!anchor) {
            for (const reg of regs) {
                console.warn(`[ui-injector] ${reg.extensionName} at ${point}: no anchor element; skipping sandboxed extension`);
            }
            return;
        }

        let anchorChannels = this.channels.get(anchor);
        if (!anchorChannels) {
            anchorChannels = new Map();
            this.channels.set(anchor, anchorChannels);
        }

        for (const reg of regs) {
            const key = `${reg.extensionId}::${point}`;
            if (anchorChannels.has(key)) continue;

            try {
                const iframe = document.createElement('iframe');
                // Per-mount channel token, delivered through the document URL so
                // only the document we mount can know it. The bridge echoes it
                // back before the host sends `init` or honours any capability
                // call (see types.ts HostHello).
                const channelToken = mintChannelToken();
                iframe.setAttribute('sandbox', 'allow-scripts');
                iframe.setAttribute('src', `${reg.moduleUrl}?extch=${channelToken}`);
                // Invisible by default — extension surfaces itself via ui.* API.
                iframe.style.cssText = 'width:100%;height:0;border:none;display:block;';
                anchor.appendChild(iframe);

                const channel = new ExtensionHostChannel({
                    extensionId: reg.extensionId,
                    extensionName: reg.extensionName,
                    point,
                    granted: reg.granted,
                    config: reg.config,
                    context,
                    anchor,
                    iframe,
                    channelToken,
                });
                anchorChannels.set(key, channel);
                // Every load: request the handshake (and disarm + warn on a
                // reload). Started after sandbox loads so nothing is missed.
                iframe.addEventListener('load', () => channel.notifyFrameLoad());
            } catch (e: any) {
                console.error(`[ui-injector] Failed to activate ${reg.extensionName} at ${point}:`, e);
            }
        }
    }

    refreshSlot(point: InjectionPoint, context: InjectionContext, anchor: HTMLElement | null = null): void {
        this.teardownSlot(point, anchor);
        this.renderSlot(point, context, anchor);
    }

    /**
     * Forward an app event to the channel mounted at `anchor` for `point`.
     * Scoped to that specific instance (e.g. a content change from one editor
     * must only reach that editor's own extension, never a sibling file's
     * preview).
     */
    forwardEvent(point: InjectionPoint, cap: string, data: any, anchor: HTMLElement | null): void {
        const ac = anchor ? this.channels.get(anchor) : undefined;
        if (!ac) return;
        for (const ch of ac.values()) {
            if (ch.point === point) ch.forward(cap, data);
        }
    }

    /**
     * Tear down channels mounted at `point`. When `anchor` is supplied, only
     * the channels at that specific instance are destroyed (used when a single
     * editor closes or its file changes); otherwise all instances at the point
     * are torn down.
     */
    teardownSlot(point: InjectionPoint, anchor?: HTMLElement | null): void {
        const teardownOne = (ac: Map<string, ExtensionHostChannel>) => {
            for (const [key, ch] of ac) {
                if (ch.point === point) {
                    ch.destroy();
                    ac.delete(key);
                }
            }
        };
        if (anchor) {
            const ac = this.channels.get(anchor);
            if (ac) {
                teardownOne(ac);
                // Drop the anchor entry once it has no channels left, so the
                // (dead) anchor element is no longer pinned in the map and can
                // be garbage-collected.
                if (ac.size === 0) this.channels.delete(anchor);
            }
        } else {
            // Iterate over a snapshot — we delete empty inner maps below.
            for (const [el, ac] of [...this.channels]) {
                teardownOne(ac);
                if (ac.size === 0) this.channels.delete(el);
            }
        }
    }

    getPointVersion(_point: InjectionPoint): number {
        return this.version;
    }

    clearCache() {
        this.destroyAll();
        this.setThemeCSS(null);
        this.hasBaseline = false;
        this.extensionsFingerprint = '';
    }

    hasContent(point: InjectionPoint): boolean {
        return this.getRegistrations(point).length > 0;
    }

    private destroyAll() {
        for (const ac of this.channels.values()) {
            for (const ch of ac.values()) ch.destroy();
        }
        this.channels.clear();
    }
}

export const uiInjector = new UIInjector();
