import type { Extension } from '$lib/types';
import { computeGrants, CAP_THEME_UPDATE } from '$lib/extensions/capabilities';
import { readThemeVars } from '$lib/extensions/handlers/theme';
import { ExtensionHostChannel } from '$lib/extensions/channel';
import {
    INJECTION_POINTS,
    type InjectionPoint,
    type InjectionContext,
} from '$lib/extensions/types';

export { INJECTION_POINTS };
export type { InjectionPoint, InjectionContext };

const THEME_CSS_KEY = 'kanban-theme-css';
const THEME_LINK_ID = 'kanban-theme-stylesheet';

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
function extBaseURL(ext: Extension): string {
    return `/extensions/${authorDir(ext.manifest.author)}/${ext.name}`;
}

// resolveAssetURL resolves a manifest asset path (e.g. a css_url) against the
// extension's served base, so a relative path like "./styles.css" or
// "styles.css" resolves to /extensions/<author>/<name>/styles.css.
function resolveAssetURL(ext: Extension, asset: string): string {
    if (!asset) return asset;
    if (/^([a-z][a-z0-9+.-]*:)?\/\//i.test(asset) || asset.startsWith('/')) {
        return asset; // absolute URL or server-root path — use as-is
    }
    return `${extBaseURL(ext)}/${asset.replace(/^\.?\//, '')}`;
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

    registerFromExtensions(extensions: Extension[]) {
        this.destroyAll();

        this.registrations = [];
        let cssUrl: string | null = null;

        for (const ext of extensions) {
            if (!ext.enabled) continue;

            // CSS-type extensions: inject global stylesheet
            if (ext.type === 'css' && ext.manifest.capabilities?.css?.css_url) {
                if (!cssUrl) {
                    cssUrl = resolveAssetURL(ext, ext.manifest.capabilities.css.css_url);
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

        try { localStorage.setItem(THEME_CSS_KEY, url); } catch {}

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
                iframe.setAttribute('sandbox', 'allow-scripts');
                iframe.setAttribute('src', reg.moduleUrl);
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
                });
                anchorChannels.set(key, channel);
                // Start channel after sandbox loads so init is not missed.
                iframe.addEventListener('load', () => channel.start());
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
            if (ac) teardownOne(ac);
        } else {
            for (const ac of this.channels.values()) teardownOne(ac);
        }
    }

    getPointVersion(_point: InjectionPoint): number {
        return this.version;
    }

    clearCache() {
        this.destroyAll();
        this.setThemeCSS(null);
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
