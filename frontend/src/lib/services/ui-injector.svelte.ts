import type { Extension } from '$lib/types';
import { computeGrants } from '$lib/extensions/capabilities';
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
    private channels = new Map<string, ExtensionHostChannel>();
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
                    cssUrl = ext.manifest.capabilities.css.css_url;
                }
                continue;
            }

            // UI-type extensions: register injection points. The entry page is
            // always index.html in the extension's own directory.
            if (ext.type !== 'ui' || !ext.manifest.capabilities?.ui) continue;
            const ui = ext.manifest.capabilities.ui;
            if (!ui.injection_points) continue;
            const moduleUrl = `/extensions/ui/${ext.name}/index.html`;

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
        document.head.appendChild(link);

        try { localStorage.setItem(THEME_CSS_KEY, url); } catch {}

        this.activeCSSUrl = url;
    }

    getRegistrations(point: InjectionPoint): InjectionRegistration[] {
        return this.registrations
            .filter(r => r.point === point)
            .sort((a, b) => a.order - b.order);
    }

    renderSlot(point: InjectionPoint, context: InjectionContext, anchor: HTMLElement | null = null): void {
        const regs = this.getRegistrations(point);
        if (regs.length === 0) return;

        for (const reg of regs) {
            const key = `${reg.extensionId}::${point}`;
            if (this.channels.has(key)) continue;

            if (!anchor) {
                console.warn(`[ui-injector] ${reg.extensionName} at ${point}: no anchor element; skipping sandboxed extension`);
                continue;
            }

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
                this.channels.set(key, channel);
                // Start channel after sandbox loads so init is not missed.
                iframe.addEventListener('load', () => channel.start());
            } catch (e: any) {
                console.error(`[ui-injector] Failed to activate ${reg.extensionName} at ${point}:`, e);
            }
        }
    }

    refreshSlot(point: InjectionPoint, context: InjectionContext, anchor: HTMLElement | null = null): void {
        this.teardownSlot(point);
        this.renderSlot(point, context, anchor);
    }

    teardownSlot(point: InjectionPoint): void {
        for (const [key, ch] of this.channels) {
            if (key.endsWith(`::${point}`)) {
                ch.destroy();
                this.channels.delete(key);
            }
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
        for (const ch of this.channels.values()) {
            ch.destroy();
        }
        this.channels.clear();
    }
}

export const uiInjector = new UIInjector();
