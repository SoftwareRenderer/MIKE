import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { UIInjector } from '$lib/services/ui-injector.svelte';
import type { Extension } from '$lib/types';

describe('UIInjector CSS extension handling', () => {
    let injector: UIInjector;

    beforeEach(() => {
        injector = new UIInjector();
        // Clear any existing theme link
        const existing = document.getElementById('kanban-theme-stylesheet');
        if (existing) existing.remove();
        // Clear localStorage
        localStorage.removeItem('kanban-theme-css');
    });

    afterEach(() => {
        injector.clearCache();
    });

    function makeCSSExtension(cssUrl: string, enabled = true): Extension {
        return {
            id: 'test-ext-1',
            name: 'test-theme',
            version: '1.0.0',
            type: 'css',
            enabled,
            manifest: {
                display_name: 'Test Theme',
                description: 'Test',
                author: 'Test',
                deprecated: false,
                css: {
                    css_url: cssUrl,
                },
            },
            config: {},
            container_config: {},
            sandbox_config: { network_isolation: false, memory_limit: 0 },
            running: false,
            installed_at: '',
            built_at: '',
            install_source: 'manual',
        };
    }

    it('injects a <link> tag when a CSS extension has css_url', () => {
        injector.registerFromExtensions([makeCSSExtension('/extensions/mike-light/styles.css')]);

        const link = document.getElementById('kanban-theme-stylesheet');
        expect(link).toBeTruthy();
        expect(link?.getAttribute('rel')).toBe('stylesheet');
        expect(link?.getAttribute('href')).toBe('/extensions/mike-light/styles.css');
    });

    it('caches the CSS URL in localStorage', () => {
        injector.registerFromExtensions([makeCSSExtension('/extensions/mike-light/styles.css')]);

        const cached = localStorage.getItem('kanban-theme-css');
        expect(cached).toBe('/extensions/mike-light/styles.css');
    });

    it('removes the <link> tag when no CSS extension is active', () => {
        // First inject
        injector.registerFromExtensions([makeCSSExtension('/extensions/mike-light/styles.css')]);
        expect(document.getElementById('kanban-theme-stylesheet')).toBeTruthy();

        // Then register with no CSS extensions
        injector.registerFromExtensions([]);

        expect(document.getElementById('kanban-theme-stylesheet')).toBeFalsy();
    });

    it('clears localStorage when CSS extension is removed', () => {
        injector.registerFromExtensions([makeCSSExtension('/extensions/mike-light/styles.css')]);
        expect(localStorage.getItem('kanban-theme-css')).toBeTruthy();

        injector.registerFromExtensions([]);
        expect(localStorage.getItem('kanban-theme-css')).toBeNull();
    });

    it('removes the stylesheet when CSS extension is disabled', () => {
        injector.registerFromExtensions([makeCSSExtension('/extensions/mike-light/styles.css', true)]);
        expect(document.getElementById('kanban-theme-stylesheet')).toBeTruthy();

        // Disable the extension
        injector.registerFromExtensions([makeCSSExtension('/extensions/mike-light/styles.css', false)]);
        expect(document.getElementById('kanban-theme-stylesheet')).toBeFalsy();
    });

    it('swaps stylesheet when css_url changes', () => {
        injector.registerFromExtensions([makeCSSExtension('/theme/dark.css')]);
        expect(document.getElementById('kanban-theme-stylesheet')?.getAttribute('href')).toBe('/theme/dark.css');

        // Create a new extension with different css_url
        const lightExt = makeCSSExtension('/theme/light.css');
        lightExt.id = 'test-ext-2';
        lightExt.name = 'mike-light';
        injector.registerFromExtensions([lightExt]);

        expect(document.getElementById('kanban-theme-stylesheet')?.getAttribute('href')).toBe('/theme/light.css');
    });

    it('does not inject for LSP extensions', () => {
        const lspExt: Extension = {
            id: 'gopls',
            name: 'gopls',
            version: '1.0.0',
            type: 'lsp',
            enabled: true,
            manifest: {
                display_name: 'Go LSP',
                description: 'Go',
                author: 'Go',
                deprecated: false,
                lsp: {
                    command: 'gopls',
                    args: ['serve'],
                    extensions: ['go'],
                    language: 'go',
                },
            },
            config: {},
            container_config: {},
            sandbox_config: { network_isolation: false, memory_limit: 0 },
            running: false,
            installed_at: '',
            built_at: '',
            install_source: 'registry',
        };

        injector.registerFromExtensions([lspExt]);
        expect(document.getElementById('kanban-theme-stylesheet')).toBeFalsy();
    });

    it('does not inject for UI extensions (they use module_url, not css_url)', () => {
        const uiExt: Extension = {
            id: 'ui-ext',
            name: 'ui-ext',
            version: '1.0.0',
            type: 'ui',
            enabled: true,
            manifest: {
                display_name: 'UI Extension',
                description: 'UI',
                author: 'Test',
                deprecated: false,
                ui: {
                    module_url: '/extensions/ui/test.js',
                    injection_points: ['task-card'],
                },
            },
            config: {},
            container_config: {},
            sandbox_config: { network_isolation: false, memory_limit: 0 },
            running: false,
            installed_at: '',
            built_at: '',
            install_source: 'manual',
        };

        injector.registerFromExtensions([uiExt]);
        expect(document.getElementById('kanban-theme-stylesheet')).toBeFalsy();
    });

    it('first enabled CSS extension with css_url wins', () => {
        const ext1 = makeCSSExtension('/theme/first.css');
        const ext2 = makeCSSExtension('/theme/second.css');
        ext2.id = 'test-ext-2';
        ext2.name = 'second-theme';

        injector.registerFromExtensions([ext1, ext2]);

        expect(document.getElementById('kanban-theme-stylesheet')?.getAttribute('href')).toBe('/theme/first.css');
    });

    it('clearCache removes the stylesheet', () => {
        injector.registerFromExtensions([makeCSSExtension('/extensions/mike-light/styles.css')]);
        expect(document.getElementById('kanban-theme-stylesheet')).toBeTruthy();

        injector.clearCache();

        expect(document.getElementById('kanban-theme-stylesheet')).toBeFalsy();
    });

    it('skips no-op when css_url is unchanged', () => {
        injector.registerFromExtensions([makeCSSExtension('/theme/same.css')]);
        const firstLink = document.getElementById('kanban-theme-stylesheet');

        injector.registerFromExtensions([makeCSSExtension('/theme/same.css')]);
        const secondLink = document.getElementById('kanban-theme-stylesheet');

        // Same element reference (not recreated)
        expect(firstLink).toBe(secondLink);
    });

    it('CSS extension does not register UI injection points', () => {
        injector.registerFromExtensions([makeCSSExtension('/theme/test.css')]);

        // CSS extensions should not create injection registrations
        expect(injector.registrations.length).toBe(0);
    });

    it('UI extension registrations use manifest priority for order', () => {
        const extLow: Extension = {
            id: 'ext-low',
            name: 'ext-low',
            version: '1.0.0',
            type: 'ui',
            enabled: true,
            manifest: {
                display_name: 'Low Priority',
                description: '',
                author: '',
                deprecated: false,
                priority: 0,
                ui: {
                    module_url: '/extensions/low.js',
                    injection_points: ['task-card'],
                },
            },
            config: {},
            container_config: {},
            sandbox_config: { network_isolation: false, memory_limit: 0 },
            running: false,
            installed_at: '',
            built_at: '',
            install_source: 'manual',
        };
        const extHigh: Extension = {
            id: 'ext-high',
            name: 'ext-high',
            version: '1.0.0',
            type: 'ui',
            enabled: true,
            manifest: {
                display_name: 'High Priority',
                description: '',
                author: '',
                deprecated: false,
                priority: 10,
                ui: {
                    module_url: '/extensions/high.js',
                    injection_points: ['task-card'],
                },
            },
            config: {},
            container_config: {},
            sandbox_config: { network_isolation: false, memory_limit: 0 },
            running: false,
            installed_at: '',
            built_at: '',
            install_source: 'manual',
        };

        // Register high-priority extension first in the array
        injector.registerFromExtensions([extHigh, extLow]);

        const regs = injector.getRegistrations('task-card' as any);
        expect(regs.length).toBe(2);
        // Lower priority value = loaded first
        expect(regs[0].extensionName).toBe('ext-low');
        expect(regs[1].extensionName).toBe('ext-high');
    });

    it('UI extension with default priority (0) loads before explicit priority 10', () => {
        const extDefault: Extension = {
            id: 'ext-default',
            name: 'ext-default',
            version: '1.0.0',
            type: 'ui',
            enabled: true,
            manifest: {
                display_name: 'Default Priority',
                description: '',
                author: '',
                deprecated: false,
                ui: {
                    module_url: '/extensions/default.js',
                    injection_points: ['task-card'],
                },
            },
            config: {},
            container_config: {},
            sandbox_config: { network_isolation: false, memory_limit: 0 },
            running: false,
            installed_at: '',
            built_at: '',
            install_source: 'manual',
        };
        const extTen: Extension = {
            id: 'ext-ten',
            name: 'ext-ten',
            version: '1.0.0',
            type: 'ui',
            enabled: true,
            manifest: {
                display_name: 'Priority 10',
                description: '',
                author: '',
                deprecated: false,
                priority: 10,
                ui: {
                    module_url: '/extensions/ten.js',
                    injection_points: ['task-card'],
                },
            },
            config: {},
            container_config: {},
            sandbox_config: { network_isolation: false, memory_limit: 0 },
            running: false,
            installed_at: '',
            built_at: '',
            install_source: 'manual',
        };

        injector.registerFromExtensions([extTen, extDefault]);

        const regs = injector.getRegistrations('task-card' as any);
        expect(regs.length).toBe(2);
        expect(regs[0].extensionName).toBe('ext-default');
        expect(regs[0].order).toBe(0);
        expect(regs[1].extensionName).toBe('ext-ten');
        expect(regs[1].order).toBe(10);
    });

    it('negative priority loads before zero priority', () => {
        const extNeg: Extension = {
            id: 'ext-neg',
            name: 'ext-neg',
            version: '1.0.0',
            type: 'ui',
            enabled: true,
            manifest: {
                display_name: 'Negative Priority',
                description: '',
                author: '',
                deprecated: false,
                priority: -5,
                ui: {
                    module_url: '/extensions/neg.js',
                    injection_points: ['task-card'],
                },
            },
            config: {},
            container_config: {},
            sandbox_config: { network_isolation: false, memory_limit: 0 },
            running: false,
            installed_at: '',
            built_at: '',
            install_source: 'manual',
        };
        const extZero: Extension = {
            id: 'ext-zero',
            name: 'ext-zero',
            version: '1.0.0',
            type: 'ui',
            enabled: true,
            manifest: {
                display_name: 'Zero Priority',
                description: '',
                author: '',
                deprecated: false,
                priority: 0,
                ui: {
                    module_url: '/extensions/zero.js',
                    injection_points: ['task-card'],
                },
            },
            config: {},
            container_config: {},
            sandbox_config: { network_isolation: false, memory_limit: 0 },
            running: false,
            installed_at: '',
            built_at: '',
            install_source: 'manual',
        };

        injector.registerFromExtensions([extZero, extNeg]);

        const regs = injector.getRegistrations('task-card' as any);
        expect(regs.length).toBe(2);
        expect(regs[0].extensionName).toBe('ext-neg');
        expect(regs[0].order).toBe(-5);
        expect(regs[1].extensionName).toBe('ext-zero');
        expect(regs[1].order).toBe(0);
    });
});
