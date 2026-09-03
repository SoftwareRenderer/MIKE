import { describe, it, expect, vi, afterEach, beforeAll } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import ExtensionsTab from '$lib/components/ConfigModal/ExtensionsTab.svelte';
import type { Extension } from '$lib/types';

/**
 * The dependency-cache row: that it appears only for extensions that declared a cache, prefills
 * editable text, sends exactly the path shown, sends an empty string on revoke, and shows a refusal
 * on its own row rather than as a page-wide banner.
 */

const HOME = '/home/tester';
const GO_ID = 'go-team-gopls';
const TS_ID = 'microsoft-typescript';

/** The gopls shape as the server reports it after task 2: declaration present, grant state field
 *  absent until the user applies one. */
function lspExtension(id: string, name: string, sandbox: Extension['sandbox_config']): Extension {
    return {
        id,
        name,
        author: 'go-team',
        version: '0.7.0',
        type: 'lsp',
        manifest: {
            display_name: name,
            description: `${name} language server`,
            capabilities: { lsp: { language: 'go', command: 'gopls' } },
        },
        config: {},
        container_config: {},
        sandbox_config: sandbox,
        install_source: 'registry',
        enabled: true,
        running: false,
        installed_at: '2026-01-01T00:00:00Z',
        built_at: '',
    } as unknown as Extension;
}

const DECLARED = { container_path: '/go/pkg/mod', env: 'GOMODCACHE', read_only: true };

interface Harness {
    /** What the store holds; the list endpoint answers from this, so a granted path shows up after reload. */
    granted: Record<string, string>;
    puts: { id: string; body: any }[];
    /** Set to make the next PUT fail with this message (the server's sentence, not a code). */
    putFailure: string | null;
}

function stubFetch(h: Harness) {
    vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
        const url = new URL(String(input), 'http://localhost');
        const json = (body: unknown, status = 200) =>
            new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

        if (url.pathname === '/api/auth/session') return json({ authenticated: true, csrf_token: 'csrf-test' });
        if (url.pathname === '/api/home') return json({ home: HOME });
        if (url.pathname === '/api/extensions/registry' || url.pathname === '/api/extensions/local') return json([]);

        if (url.pathname === '/api/extensions' && (!init?.method || init.method === 'GET')) {
            return json([
                lspExtension(GO_ID, 'gopls', {
                    network_isolation: true,
                    memory_limit: 0,
                    dependency_cache: DECLARED,
                    dependency_host_path: h.granted[GO_ID] || '',
                }),
                // A server whose dependencies live in the checkout: it declares nothing, so it must
                // not be offered a directory it would never read.
                lspExtension(TS_ID, 'typescript', { network_isolation: true, memory_limit: 0 }),
            ]);
        }

        const put = url.pathname.match(/^\/api\/extensions\/([^/]+)\/dependency-cache$/);
        if (put && (init?.method === 'PUT' || init?.method === 'PATCH')) {
            const body = JSON.parse(String(init.body || '{}'));
            h.puts.push({ id: decodeURIComponent(put[1]), body });
            if (h.putFailure) {
                const message = h.putFailure;
                return json({ error: message }, 400);
            }
            h.granted[decodeURIComponent(put[1])] = (body.host_path || '').trim();
            return json({ ok: true });
        }

        return json({ error: `unexpected request ${init?.method || 'GET'} ${url.pathname}` }, 404);
    }));
}

function newHarness(): Harness {
    return { granted: {}, puts: [], putFailure: null };
}

async function renderTab() {
    const utils = render(ExtensionsTab, { props: { onExtensionsChanged: () => {} } });
    await waitFor(() => {
        expect(utils.container.querySelector('.ext-name')).toBeTruthy();
    });
    return utils;
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

describe('Settings → Extensions: the dependency directory', () => {
    it('asks only of the extension that declared a cache', async () => {
        const h = newHarness();
        stubFetch(h);
        const { container } = await renderTab();

        const rows = container.querySelectorAll('.ext-dep');
        expect(rows).toHaveLength(1);

        // Named by its own declaration, so two LSP rows can never be confused for each other.
        const label = rows[0].querySelector('.ext-dep-input')?.getAttribute('aria-label') || '';
        expect(label).toContain('gopls');

        // Where it looks inside the sandbox is stated, because that is the half the manifest owns.
        expect(rows[0].querySelector('.ext-dep-hint')?.textContent).toContain('GOMODCACHE=/go/pkg/mod');
    });

    it('prefills the suggestion as editable text, and nothing is granted by it', async () => {
        const h = newHarness();
        stubFetch(h);
        const { container } = await renderTab();

        const input = container.querySelector('.ext-dep-input') as HTMLInputElement;
        // The suggestion arrives on the far side of two fetches (the list, then the home
        // directory), so it is waited for rather than assumed — and it must arrive as text in the
        // field, where it is readable and editable, not as a value waiting to be applied.
        await waitFor(() => expect(input.value).toBe(`${HOME}/go/pkg/mod`));
        // A suggestion is not a grant: the state line still says nothing is shared, and no request
        // has been made on the user's behalf.
        expect(container.querySelector('.ext-dep-none')).toBeTruthy();
        expect(h.puts).toEqual([]);
    });

    it('applies the path shown in the field, and reports it afterwards', async () => {
        const h = newHarness();
        stubFetch(h);
        const { container } = await renderTab();

        const input = container.querySelector('.ext-dep-input') as HTMLInputElement;
        input.value = '/data/shared/gomodcache';
        input.dispatchEvent(new Event('input', { bubbles: true }));
        await fireEvent.click([...container.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Apply')!);

        await waitFor(() => expect(h.puts).toHaveLength(1));
        expect(h.puts[0].id).toBe(GO_ID);
        expect(h.puts[0].body).toEqual({ host_path: '/data/shared/gomodcache' });

        // The row has to show the applied state, or the next person cannot tell whether it is set.
        await waitFor(() => expect(container.querySelector('.ext-dep-path')?.textContent).toBe('/data/shared/gomodcache'));
        expect(container.querySelector('.ext-dep-none')).toBeNull();
        // And the button that made it say "Apply" is now the one that changes it.
        expect([...container.querySelectorAll('button')].some((b) => b.textContent?.trim() === 'Change')).toBe(true);
    });

    it('revokes with an empty path, which is a value and not an omission', async () => {
        const h = newHarness();
        h.granted[GO_ID] = '/data/shared/gomodcache';
        stubFetch(h);
        const { container } = await renderTab();

        await waitFor(() => expect(container.querySelector('.ext-dep-path')?.textContent).toBe('/data/shared/gomodcache'));
        await fireEvent.click([...container.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Revoke')!);

        await waitFor(() => expect(h.puts).toHaveLength(1));
        expect(h.puts[0].body).toEqual({ host_path: '' });
        await waitFor(() => expect(container.querySelector('.ext-dep-none')).toBeTruthy());
    });

    it('puts the server’s refusal on the row that caused it', async () => {
        const h = newHarness();
        h.putFailure = '/nope cannot be read (create it first — a task running the toolchain\'s download command is the usual way)';
        stubFetch(h);
        const { container } = await renderTab();

        // Typed rather than left at the suggestion: a refusal has to surface wherever the path came
        // from, and typing removes any doubt about which value was sent.
        const input = container.querySelector('.ext-dep-input') as HTMLInputElement;
        input.value = '/nope';
        input.dispatchEvent(new Event('input', { bubbles: true }));
        await fireEvent.click([...container.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Apply')!);

        await waitFor(() => expect(container.querySelector('.ext-dep-error')?.textContent).toContain('cannot be read'));
        // The failure is per-row: no page-wide banner, and the state stays what it was.
        expect(container.querySelector('.error-banner')).toBeNull();
        expect(container.querySelector('.ext-dep-path')).toBeNull();
    });

    it('will not apply an empty field', async () => {
        const h = newHarness();
        stubFetch(h);
        const { container } = await renderTab();

        const input = container.querySelector('.ext-dep-input') as HTMLInputElement;
        input.value = '   ';
        input.dispatchEvent(new Event('input', { bubbles: true }));

        const apply = [...container.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Apply') as HTMLButtonElement;
        expect(apply.disabled).toBe(true);
        expect(h.puts).toEqual([]);
    });
});
