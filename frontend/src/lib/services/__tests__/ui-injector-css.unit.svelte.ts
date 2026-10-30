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
                version: '1.0.0',
                capabilities: {
                    css: {
                        css_url: cssUrl,
                    },
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
        injector.registerFromExtensions([makeCSSExtension('styles.css')]);

        const link = document.getElementById('kanban-theme-stylesheet');
        expect(link).toBeTruthy();
        expect(link?.getAttribute('rel')).toBe('stylesheet');
        expect(link?.getAttribute('href')).toBe('/extensions/test/test-theme/styles.css');
    });

    it('caches the CSS URL in localStorage', () => {
        injector.registerFromExtensions([makeCSSExtension('styles.css')]);

        const cached = localStorage.getItem('kanban-theme-css');
        expect(cached).toBe('/extensions/test/test-theme/styles.css');
    });

    it('resolves a leading-slash css_url against the extension base (not server root)', () => {
        injector.registerFromExtensions([makeCSSExtension('/styles.css')]);

        const link = document.getElementById('kanban-theme-stylesheet');
        expect(link?.getAttribute('href')).toBe('/extensions/test/test-theme/styles.css');
    });

    it('rejects an external (https) css_url — no stylesheet injected', () => {
        injector.registerFromExtensions([makeCSSExtension('https://evil.example/theme.css')]);

        expect(document.getElementById('kanban-theme-stylesheet')).toBeFalsy();
        expect(localStorage.getItem('kanban-theme-css')).toBeNull();
    });

    it('a rejected css_url does not block a subsequent valid CSS extension', () => {
        const bad = makeCSSExtension('https://evil.example/theme.css');
        const good = makeCSSExtension('styles.css');
        good.id = 'test-ext-2';
        good.name = 'good-theme';

        injector.registerFromExtensions([bad, good]);

        expect(document.getElementById('kanban-theme-stylesheet')?.getAttribute('href')).toBe('/extensions/test/good-theme/styles.css');
    });

    it('removes the <link> tag when no CSS extension is active', () => {
        // First inject
        injector.registerFromExtensions([makeCSSExtension('styles.css')]);
        expect(document.getElementById('kanban-theme-stylesheet')).toBeTruthy();

        // Then register with no CSS extensions
        injector.registerFromExtensions([]);

        expect(document.getElementById('kanban-theme-stylesheet')).toBeFalsy();
    });

    it('clears localStorage when CSS extension is removed', () => {
        injector.registerFromExtensions([makeCSSExtension('styles.css')]);
        expect(localStorage.getItem('kanban-theme-css')).toBeTruthy();

        injector.registerFromExtensions([]);
        expect(localStorage.getItem('kanban-theme-css')).toBeNull();
    });

    it('removes the stylesheet when CSS extension is disabled', () => {
        injector.registerFromExtensions([makeCSSExtension('styles.css', true)]);
        expect(document.getElementById('kanban-theme-stylesheet')).toBeTruthy();

        // Disable the extension
        injector.registerFromExtensions([makeCSSExtension('styles.css', false)]);
        expect(document.getElementById('kanban-theme-stylesheet')).toBeFalsy();
    });

    it('swaps stylesheet when css_url changes', () => {
        injector.registerFromExtensions([makeCSSExtension('theme/dark.css')]);
        expect(document.getElementById('kanban-theme-stylesheet')?.getAttribute('href')).toBe('/extensions/test/test-theme/theme/dark.css');

        // Create a new extension with different css_url
        const lightExt = makeCSSExtension('theme/light.css');
        lightExt.id = 'test-ext-2';
        lightExt.name = 'mike-light';
        injector.registerFromExtensions([lightExt]);

        expect(document.getElementById('kanban-theme-stylesheet')?.getAttribute('href')).toBe('/extensions/test/mike-light/theme/light.css');
    });

    it('does not inject for UI extensions (they use index.html, not css_url)', () => {
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
                version: '1.0.0',
                capabilities: {
                    ui: {
                        injection_points: ['taskCard'],
                    },
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
        const ext1 = makeCSSExtension('theme/first.css');
        const ext2 = makeCSSExtension('theme/second.css');
        ext2.id = 'test-ext-2';
        ext2.name = 'second-theme';

        injector.registerFromExtensions([ext1, ext2]);

        expect(document.getElementById('kanban-theme-stylesheet')?.getAttribute('href')).toBe('/extensions/test/test-theme/theme/first.css');
    });

    it('clearCache removes the stylesheet', () => {
        injector.registerFromExtensions([makeCSSExtension('styles.css')]);
        expect(document.getElementById('kanban-theme-stylesheet')).toBeTruthy();

        injector.clearCache();

        expect(document.getElementById('kanban-theme-stylesheet')).toBeFalsy();
    });

    it('skips no-op when css_url is unchanged', () => {
        injector.registerFromExtensions([makeCSSExtension('theme/same.css')]);
        const firstLink = document.getElementById('kanban-theme-stylesheet');

        injector.registerFromExtensions([makeCSSExtension('theme/same.css')]);
        const secondLink = document.getElementById('kanban-theme-stylesheet');

        // Same element reference (not recreated)
        expect(firstLink).toBe(secondLink);
    });

    it('CSS extension does not register UI injection points', () => {
        injector.registerFromExtensions([makeCSSExtension('theme/test.css')]);

        // CSS extensions should not create injection registrations
        expect(injector.registrations.length).toBe(0);
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
                version: '1.0.0',
                capabilities: {
                    ui: {
                        injection_points: ['taskCard'],
                    },
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
                version: '1.0.0',
                priority: 10,
                capabilities: {
                    ui: {
                        injection_points: ['taskCard'],
                    },
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

        const regs = injector.getRegistrations('taskCard' as any);
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
                version: '1.0.0',
                priority: -5,
                capabilities: {
                    ui: {
                        injection_points: ['taskCard'],
                    },
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
                version: '1.0.0',
                priority: 0,
                capabilities: {
                    ui: {
                        injection_points: ['taskCard'],
                    },
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

        const regs = injector.getRegistrations('taskCard' as any);
        expect(regs.length).toBe(2);
        expect(regs[0].extensionName).toBe('ext-neg');
        expect(regs[0].order).toBe(-5);
        expect(regs[1].extensionName).toBe('ext-zero');
        expect(regs[1].order).toBe(0);
    });
});
