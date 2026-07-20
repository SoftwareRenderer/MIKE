import type { Extension } from '$lib/types';

/** Named injection points where UI extensions can inject content. */
export const INJECTION_POINTS = {
    CARD_HEADER: 'card-header',
    CARD_FOOTER: 'card-footer',
    EDITOR_TOOLBAR: 'editor-toolbar',
    BOARD_SECTION: 'board-section',
    TASK_CHAT_HEADER: 'task-chat-header',
    TASK_CHAT_FOOTER: 'task-chat-footer',
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
}

/**
 * UIInjector manages UI extension injection points.
 * It loads registered UI extensions and renders their content at named slots.
 */
export class UIInjector {
    /** Registered injections. */
    registrations = $state<InjectionRegistration[]>([]);
    /** Loaded modules (cached). */
    private modules = new Map<string, any>();
    /** Loading state. */
    loading = $state(false);
    /** Error state. */
    error = $state('');

    /** Register injections from UI extensions. */
    registerFromExtensions(extensions: Extension[]) {
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
     * Render content for an injection point.
     * Returns an array of rendered components (as strings for now).
     * In the full implementation, this would dynamically import and render Svelte components.
     */
    async renderSlot(point: InjectionPoint, context: InjectionContext): Promise<any[]> {
        const regs = this.getRegistrations(point);
        if (regs.length === 0) return [];

        const results = [];
        for (const reg of regs) {
            try {
                // Load module from cache or fetch
                let mod = this.modules.get(reg.moduleUrl);
                if (!mod) {
                    // In production, this would be a dynamic import
                    // For now, we log that an injection point is active
                    console.log(`[ui-injector] Would load module ${reg.moduleUrl} for ${reg.extensionName} at ${point}`);
                    mod = { default: null };
                    this.modules.set(reg.moduleUrl, mod);
                }

                // Render the component with context
                if (mod?.default) {
                    results.push({
                        extensionId: reg.extensionId,
                        extensionName: reg.extensionName,
                        component: mod.default,
                        context,
                    });
                }
            } catch (e: any) {
                console.error(`[ui-injector] Failed to render ${reg.extensionName} at ${point}:`, e);
                this.error = e.message;
            }
        }

        return results;
    }

    /** Clear loaded modules (for hot reload). */
    clearCache() {
        this.modules.clear();
    }

    /** Check if an injection point has any registrations. */
    hasContent(point: InjectionPoint): boolean {
        return this.getRegistrations(point).length > 0;
    }
}

// Singleton instance
export const uiInjector = new UIInjector();
