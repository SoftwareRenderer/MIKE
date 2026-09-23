import { describe, it, expect, afterEach, beforeAll, vi } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import ExtensionsTab from '$lib/components/ConfigModal/ExtensionsTab.svelte';
import type { Extension, ExtensionType, RegistryEntry } from '$lib/types';

/**
 * The category badges under the search input narrow the list the text query already matched. They are a
 * shortcut, not a second source of truth: the local-first sort, the featured pin and the empty states all
 * stay as their own resolvers decide them, and the counts say what one more click would show.
 */

const INSTALLED: Extension[] = [
    installed('gopls', 'lsp'),
    installed('rust-analyzer', 'lsp'),
    installed('dark-theme', 'css'),
    installed('pi', 'agent'),
];

const REGISTRY: RegistryEntry[] = [
    entry('pylsp', 'lsp'),
    entry('ruff', 'lsp'),
    entry('prettier', 'ui'),
    entry('de-language', 'i18n'),
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

function entry(name: string, type: ExtensionType): RegistryEntry {
    return {
        name,
        author: 'Acme',
        version: '1.0.0',
        type,
        display_name: name,
        description: `${name} description`,
    };
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

async function renderTab(rows: Extension[] = INSTALLED, registry: RegistryEntry[] = REGISTRY) {
    stubFetch(rows, registry);
    const utils = render(ExtensionsTab);
    await waitFor(() => expect(utils.container.querySelector('.ext-name, .empty-state')).toBeTruthy());
    return utils;
}

/** Badge labels with their counts, in the order the row renders them. */
function badges(container: HTMLElement): string[] {
    return [...container.querySelectorAll('.category-badge')].map((b) => b.getAttribute('aria-label') ?? '');
}

function badgeOf(container: HTMLElement, label: string): HTMLButtonElement {
    const found = [...container.querySelectorAll<HTMLButtonElement>('.category-badge')]
        .find((b) => b.getAttribute('aria-label')?.includes(` ${label} `));
    if (!found) throw new Error(`no ${label} badge in: ${badges(container).join(' | ')}`);
    return found;
}

function cardNames(container: HTMLElement): string[] {
    return [...container.querySelectorAll('.extension-card')]
        .map((c) => c.querySelector('.ext-name')?.textContent?.trim() ?? '');
}

async function clickBadge(container: HTMLElement, label: string) {
    await fireEvent.click(badgeOf(container, label));
}

async function typeQuery(container: HTMLElement, value: string) {
    const input = container.querySelector('input[type="search"]') as HTMLInputElement;
    await fireEvent.input(input, { target: { value } });
}

async function openBrowse(container: HTMLElement) {
    const tab = [...container.querySelectorAll('.view-tab')].find((b) => b.textContent?.includes('Browse'));
    if (!tab) throw new Error('the Extensions tab has no Browse view');
    await fireEvent.click(tab);
    await waitFor(() => expect(container.querySelector('.registry-card')).toBeTruthy());
}

function clearButton(container: HTMLElement): HTMLButtonElement {
    const button = container.querySelector<HTMLButtonElement>('.category-clear');
    if (!button) throw new Error('no way to clear the category selection');
    return button;
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
});

describe('the category row', () => {
    it('offers nothing when the list is empty, so an empty Installed view keeps its own message', async () => {
        const { container } = await renderTab([]);
        expect(container.querySelector('.category-row')).toBeNull();
        expect(container.querySelector('.empty-state')).toBeTruthy();
    });

    it('offers one badge per category the list holds, with its count, in the fixed order', async () => {
        const { container } = await renderTab();
        expect(badges(container)).toEqual([
            'Show only LSP extensions (2)',
            'Show only AGENT extensions (1)',
            'Show only CSS extensions (1)',
        ]);
        expect(container.querySelector('.category-clear')).toBeNull();
    });

    it('writes the category alone on the badge, so the number stays in the accessible name', async () => {
        const { container } = await renderTab();
        expect([...container.querySelectorAll('.category-badge')]
            .map((b) => b.textContent?.trim())).toEqual(['LSP', 'AGENT', 'CSS']);
    });

    it('narrows the installed list to the clicked category and marks the badge pressed', async () => {
        const { container } = await renderTab();
        await clickBadge(container, 'LSP');
        expect(cardNames(container)).toEqual(['gopls', 'rust-analyzer']);
        expect(badgeOf(container, 'LSP').getAttribute('aria-pressed')).toBe('true');
        // The others keep their counts: they say what clicking them would add.
        expect(badges(container)).toContain('Show only CSS extensions (1)');
    });

    it('restores the whole list when the same badge is clicked again', async () => {
        const { container } = await renderTab();
        await clickBadge(container, 'LSP');
        await clickBadge(container, 'LSP');
        expect(cardNames(container)).toEqual(['gopls', 'rust-analyzer', 'dark-theme', 'pi']);
        expect(container.querySelector('.category-clear')).toBeNull();
    });

    it('shows both categories when two are selected', async () => {
        const { container } = await renderTab();
        await clickBadge(container, 'LSP');
        await clickBadge(container, 'CSS');
        expect(cardNames(container)).toEqual(['gopls', 'rust-analyzer', 'dark-theme']);
    });

    it('clears every selection through the one Clear control', async () => {
        const { container } = await renderTab();
        await clickBadge(container, 'LSP');
        await clickBadge(container, 'AGENT');
        await fireEvent.click(clearButton(container));
        expect(cardNames(container)).toEqual(['gopls', 'rust-analyzer', 'dark-theme', 'pi']);
        expect([...container.querySelectorAll('[aria-pressed="true"]')]).toHaveLength(0);
    });

    it('composes with the text query, which also drives the counts', async () => {
        const { container } = await renderTab();
        await typeQuery(container, 'r');
        // rust-analyzer and dark-theme: the query runs first, so the counts count only what it left.
        expect(badges(container)).toEqual([
            'Show only LSP extensions (1)',
            'Show only CSS extensions (1)',
        ]);
        await clickBadge(container, 'LSP');
        expect(cardNames(container)).toEqual(['rust-analyzer']);
    });
});

describe('the same selection under Browse', () => {
    it('narrows the registry list and re-counts over what the registry holds', async () => {
        const { container } = await renderTab();
        await clickBadge(container, 'LSP');
        await openBrowse(container);
        expect(cardNames(container)).toEqual(['pylsp', 'ruff']);
        expect(badges(container)).toEqual([
            'Show only LSP extensions (2)',
            'Show only UI extensions (1)',
            'Show only I18N extensions (1)',
        ]);
    });

    it('keeps the way out visible when a later query leaves the selection matching nothing', async () => {
        const { container } = await renderTab();
        await openBrowse(container);
        await clickBadge(container, 'UI');
        expect(cardNames(container)).toEqual(['prettier']);
        await typeQuery(container, 'ru');
        // The badge row counts what the query left, so only LSP is offered — and the selected UI badge is
        // gone with its count. Clear is what remains, and it remains reachable.
        expect(cardNames(container)).toHaveLength(0);
        expect(container.querySelector('.empty-state')).toBeTruthy();
        expect(badges(container)).toEqual(['Show only LSP extensions (1)']);
        await fireEvent.click(clearButton(container));
        expect(cardNames(container)).toEqual(['ruff']);
    });
});
