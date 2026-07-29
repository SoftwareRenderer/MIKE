import type { Extension } from '$lib/types';

/** localStorage key for cached theme CSS URL (read by inline script in index.html). */
const THEME_CSS_KEY = 'kanban-theme-css';
/** ID of the injected theme <link> element. */
const THEME_LINK_ID = 'kanban-theme-stylesheet';

/** Named injection points where UI extensions can inject content. */
export const INJECTION_POINTS = {
    TASK_CARD: 'task-card',
    FILE_EDITOR: 'file-editor',
    PROJECT_BOARD: 'project-board',
    TASK_CHAT: 'task-chat',
    PROJECT_SIDEBAR: 'project-sidebar',
    FILE_BROWSER: 'file-browser',
    CREATE_TASK: 'create-task',
} as const;

export type InjectionPoint = (typeof INJECTION_POINTS)[keyof typeof INJECTION_POINTS];

/** Context data passed to UI extension renderers. */
export interface InjectionContext {
    /** The component where the injection point is located. */
    component: string;
    /** Task ID if available. */
    taskId?: string;
    /** Project ID if available. */
    projectId?: string;
    /** File path if available (for editor injections). */
    filePath?: string;
    /** Additional context data. */
    [key: string]: any;
}

/** A registered UI extension injection. */
export interface InjectionRegistration {
    /** Extension ID. */
    extensionId: string;
    /** Extension name. */
    extensionName: string;
    /** Injection point name. */
    point: InjectionPoint;
    /** Module URL for dynamic import. */
    moduleUrl: string;
    /** Order (lower = rendered first). */
    order: number;
    /** Extension configuration. */
    config: any;
}

/**
 * UIInjector manages UI extension injection points.
 * It loads registered UI extensions and renders their content at named slots.
 *
 * UI extensions are plain JS modules that export a default `init(ctx)` function.
 * The function receives context + config, sets up DOM/event listeners,
 * and returns a cleanup function. No Svelte component rendering needed.
 */
export class UIInjector {
    /** Registered injections. */
    registrations = $state<InjectionRegistration[]>([]);
    /** Loaded modules (cached). */
    private modules = new Map<string, any>();
    /** Active JS extension cleanup functions, keyed by "extensionId::point". */
    private cleanups = new Map<string, () => void>();
    /** Global version counter — incremented on every registration change. */
    private version = $state(0);
    /** Currently active theme CSS URL (from extension css_url). */
    private activeCSSUrl = $state<string | null>(null);

    /** Register injections from UI extensions and CSS from CSS-type extensions. */
    registerFromExtensions(extensions: Extension[]) {
        for (const cleanup of this.cleanups.values()) {
            try { cleanup(); } catch {}
        }
        this.cleanups.clear();

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

            // UI-type extensions: register injection points
            if (ext.type !== 'ui' || !ext.manifest.capabilities?.ui) continue;
            const ui = ext.manifest.capabilities.ui;
            if (!ui.injection_points || !ui.module_url) continue;

            for (let i = 0; i < ui.injection_points.length; i++) {
                this.registrations.push({
                    extensionId: ext.id,
                    extensionName: ext.name,
                    point: ui.injection_points[i] as InjectionPoint,
                    moduleUrl: ui.module_url,
                    order: ext.manifest.priority ?? 0,
                    config: ext.config,
                });
            }
        }

        // Apply or remove global theme stylesheet
        this.setThemeCSS(cssUrl);

        // Bump global version so all $effect watchers re-run
        this.version = this.version + 1;
    }

    /**
     * Set the global theme stylesheet from an extension's css_url.
     * Injects a <link> tag and caches the URL in localStorage for zero-flash reloads.
     * Pass null to remove any active theme stylesheet.
     */
    private setThemeCSS(url: string | null) {
        // No change if URL is the same AND no stale link exists in the DOM
        // (a stale link can exist if the inline cache script ran before the
        //  Svelte app initialized, e.g. after an extension was disabled).
        const existing = document.getElementById(THEME_LINK_ID);
        if (url === this.activeCSSUrl && (!existing || url)) return;

        // Remove existing link
        if (existing) {
            existing.remove();
        }

        if (!url) {
            this.activeCSSUrl = null;
            try { localStorage.removeItem(THEME_CSS_KEY); } catch {}
            return;
        }

        // Inject new <link> tag
        const link = document.createElement('link');
        link.rel = 'stylesheet';
        link.href = url;
        link.id = THEME_LINK_ID;
        document.head.appendChild(link);

        // Cache for zero-flash on next page load
        try { localStorage.setItem(THEME_CSS_KEY, url); } catch {}

        this.activeCSSUrl = url;
    }

    /** Get registrations for a specific injection point. */
    getRegistrations(point: InjectionPoint): InjectionRegistration[] {
        return this.registrations
            .filter(r => r.point === point)
            .sort((a, b) => a.order - b.order);
    }

    /**
     * Activate extensions for an injection point.
     * Loads each extension module and calls its `init(ctx)` function.
     * Returns an empty array — JS extensions manage their own DOM.
     */
    async renderSlot(point: InjectionPoint, context: InjectionContext): Promise<any[]> {
        const regs = this.getRegistrations(point);
        if (regs.length === 0) return [];

        for (const reg of regs) {
            try {
                // Load module from cache or fetch
                let mod = this.modules.get(reg.moduleUrl);
                if (!mod) {
                    mod = await import(reg.moduleUrl);
                    this.modules.set(reg.moduleUrl, mod);
                }

                // Call init function (plain JS extension)
                if (typeof mod?.default === 'function') {
                    const cleanup = mod.default({ ...context, config: reg.config, extensionId: reg.extensionId });
                    if (typeof cleanup === 'function') {
                        this.cleanups.set(`${reg.extensionId}::${point}`, cleanup);
                    }
                }
            } catch (e: any) {
                console.error(`[ui-injector] Failed to activate ${reg.extensionName} at ${point}:`, e);
            }
        }

        return [];
    }

    /**
     * Get the global version counter.
     * Components use this in $effect to re-render slots when extensions change.
     */
    getPointVersion(_point: InjectionPoint): number {
        return this.version;
    }

    /**
     * Refresh an injection point: run cleanups for extensions at that point,
     * then re-activate extensions with the given context.
     * Called by components in $effect when getPointVersion() changes.
     * Note: does NOT bump the version — that's done by registerFromExtensions.
     */
    async refreshSlot(point: InjectionPoint, context: InjectionContext): Promise<any[]> {
        // Run cleanups for this specific point
        for (const [key, cleanup] of this.cleanups) {
            if (key.endsWith(`::${point}`)) {
                try { cleanup(); } catch {}
                this.cleanups.delete(key);
            }
        }
        // Re-activate
        return this.renderSlot(point, context);
    }

    /** Clear loaded modules (for hot reload). */
    clearCache() {
        this.modules.clear();
        for (const cleanup of this.cleanups.values()) {
            try { cleanup(); } catch {}
        }
        this.cleanups.clear();
        this.setThemeCSS(null);
    }

    /** Check if an injection point has any registrations. */
    hasContent(point: InjectionPoint): boolean {
        return this.getRegistrations(point).length > 0;
    }
}

// Singleton instance
export const uiInjector = new UIInjector();
