import { describe, it, expect, afterEach, beforeAll, vi } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import ExtensionsTab from '$lib/components/ConfigModal/ExtensionsTab.svelte';
import type { Extension, ExtensionType, RegistryEntry } from '$lib/types';

/**
 * The Browse view puts the translation pack for the browser's own language directly after the
 * featured entries, so the one row a `zh-CN` visitor came to Settings for is not buried at its
 * registry index position. The order itself is decided by `orderBrowseEntries`; these cases prove
 * the wiring — that the tab hands the resolver the *browser's* tag, and that an installed pack is
 * not promoted.
 */

const REGISTRY: RegistryEntry[] = [
    entry('pylsp', 'lsp'),
    entry('featured-ui', 'ui', { featured: true }),
    pack('pack-zh', 'zh'),
    pack('pack-de', 'de'),
    entry('prettier', 'ui'),
];

function installed(name: string, type: ExtensionType): Extension {
    return {
        id: `acme-${name}`,
        name,
        author: 'Acme',
        version: '1.0.0',
        type,
        manifest: { display_name: name, description: `${name} description`, author: 'Acme', version: '1.0.0' },
        config: {},
        container_config: {},
        sandbox_config: {},
        install_source: 'registry',
        enabled: true,
        running: false,
        installed_at: '2026-01-01T00:00:00Z',
        built_at: '',
    } as unknown as Extension;
}

function entry(name: string, type: ExtensionType, extra: Partial<RegistryEntry> = {}): RegistryEntry {
    return {
        name,
        author: 'Acme',
        version: '1.0.0',
        type,
        display_name: name,
        description: `${name} description`,
        ...extra,
    };
}

function pack(name: string, locale: string): RegistryEntry {
    return entry(name, 'i18n', { capabilities: { i18n: { locale, messages_url: 'catalog.json' } } });
}

function stubFetch(rows: Extension[], registry: RegistryEntry[]) {
    vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
        const url = new URL(String(input), 'http://localhost');
        const json = (body: unknown, status = 200) =>
            new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

        if (url.pathname === '/api/auth/session') return json({ authenticated: true, csrf_token: 'csrf-test' });
        if (url.pathname === '/api/home') return json({ home: '/home/tester' });
        if (url.pathname === '/api/extensions/registry') return json(registry);
        if (url.pathname === '/api/extensions/local') return json([]);
        if (url.pathname === '/api/extensions' && (!init?.method || init.method === 'GET')) return json(rows);
        return json({ error: `unexpected request ${init?.method || 'GET'} ${url.pathname}` }, 404);
    }));
}

/**
 * `navigator.language` is a getter on jsdom's Navigator, so the value is shadowed with an own
 * property and taken back afterwards — replacing the whole object would take `userAgent`,
 * `clipboard` and everything else a component under test may reach for.
 */
function withBrowserLanguage(tag: string, run: () => Promise<void>) {
    Object.defineProperty(window.navigator, 'language', { value: tag, configurable: true });
    return run().finally(() => {
        delete (window.navigator as { language?: unknown }).language;
    });
}

async function renderBrowse(rows: Extension[], registry: RegistryEntry[]) {
    stubFetch(rows, registry);
    const utils = render(ExtensionsTab);
    await waitFor(() => expect(utils.container.querySelector('.ext-name, .empty-state')).toBeTruthy());
    const tab = [...utils.container.querySelectorAll('.view-tab')].find((b) => b.textContent?.includes('Browse'));
    if (!tab) throw new Error('the Extensions tab has no Browse view');
    await fireEvent.click(tab);
    await waitFor(() => expect(utils.container.querySelectorAll('.registry-card').length).toBe(registry.length));
    return utils.container;
}

function browseOrder(container: HTMLElement): string[] {
    return [...container.querySelectorAll('.registry-card')]
        .map((c) => c.querySelector('.ext-name')?.textContent?.trim() ?? '');
}

beforeAll(() => {
    vi.stubGlobal('matchMedia', (query: string) => ({
        matches: false, media: query, onchange: null,
        addEventListener: () => {}, removeEventListener: () => {},
        addListener: () => {}, removeListener: () => {},
        dispatchEvent: () => false,
    }));
});

afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    delete (window.navigator as { language?: unknown }).language;
});

describe('the Browse view and the browser language', () => {
    it('lists the pack for the browser language right after the featured entry', async () => {
        await withBrowserLanguage('zh-CN', async () => {
            const container = await renderBrowse([], REGISTRY);
            expect(browseOrder(container)).toEqual([
                'featured-ui', 'pack-zh', 'pylsp', 'pack-de', 'prettier',
            ]);
        });
    });

    it('lists another language pack for another browser language', async () => {
        await withBrowserLanguage('de-AT', async () => {
            const container = await renderBrowse([], REGISTRY);
            expect(browseOrder(container)).toEqual([
                'featured-ui', 'pack-de', 'pylsp', 'pack-zh', 'prettier',
            ]);
        });
    });

    it('keeps the registry order for a browser that speaks the fallback locale', async () => {
        await withBrowserLanguage('en-US', async () => {
            const container = await renderBrowse([], REGISTRY);
            expect(browseOrder(container)).toEqual([
                'featured-ui', 'pylsp', 'pack-zh', 'pack-de', 'prettier',
            ]);
        });
    });

    it('does not promote a pack for the browser language that is already installed', async () => {
        await withBrowserLanguage('zh-CN', async () => {
            const container = await renderBrowse([installed('pack-zh', 'i18n')], REGISTRY);
            expect(browseOrder(container)).toEqual([
                'featured-ui', 'pylsp', 'pack-zh', 'pack-de', 'prettier',
            ]);
        });
    });
});
