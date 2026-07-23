import type { Extension } from '$lib/types';

/** Named injection points where UI extensions can inject content. */
export const INJECTION_POINTS = {
    CARD_HEADER: 'card-header',
    CARD_FOOTER: 'card-footer',
    CARD_ACTIONS: 'card-actions',
    EDITOR_TOOLBAR: 'editor-toolbar',
    BOARD_SECTION: 'board-section',
    TASK_CHAT_HEADER: 'task-chat-header',
    TASK_CHAT_FOOTER: 'task-chat-footer',
    TASK_CHAT_INPUT: 'task-chat-input',
    PROJECT_SIDEBAR: 'project-sidebar',
    SETTINGS_TAB: 'settings-tab',
    FILE_BROWSER_TOOLBAR: 'file-browser-toolbar',
    CREATE_TASK_DESCRIPTION: 'create-task-description',
    CREATE_TASK_ACTIONS: 'create-task-actions',
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
    /** Active JS extension cleanup functions. */
    private cleanups = new Map<string, () => void>();

    /** Register injections from UI extensions. */
    registerFromExtensions(extensions: Extension[]) {
        // Clean up old extensions
        for (const cleanup of this.cleanups.values()) {
            try { cleanup(); } catch {}
        }
        this.cleanups.clear();

        this.registrations = [];
        for (const ext of extensions) {
            if (!ext.enabled || ext.type !== 'ui' || !ext.manifest.ui) continue;
            const ui = ext.manifest.ui;
            if (!ui.injection_points || !ui.module_url) continue;

            for (let i = 0; i < ui.injection_points.length; i++) {
                this.registrations.push({
                    extensionId: ext.id,
                    extensionName: ext.name,
                    point: ui.injection_points[i] as InjectionPoint,
                    moduleUrl: ui.module_url,
                    order: i,
                    config: ext.config,
                });
            }
        }
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
                    const cleanup = mod.default({ ...context, config: reg.config });
                    if (typeof cleanup === 'function') {
                        this.cleanups.set(reg.extensionId, cleanup);
                    }
                }
            } catch (e: any) {
                console.error(`[ui-injector] Failed to activate ${reg.extensionName} at ${point}:`, e);
            }
        }

        return [];
    }

    /** Clear loaded modules (for hot reload). */
    clearCache() {
        this.modules.clear();
        for (const cleanup of this.cleanups.values()) {
            try { cleanup(); } catch {}
        }
        this.cleanups.clear();
    }

    /** Check if an injection point has any registrations. */
    hasContent(point: InjectionPoint): boolean {
        return this.getRegistrations(point).length > 0;
    }
}

// Singleton instance
export const uiInjector = new UIInjector();
