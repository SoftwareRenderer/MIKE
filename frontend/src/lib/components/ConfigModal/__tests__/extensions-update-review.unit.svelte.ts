import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';

const { showConfirm } = vi.hoisted(() => ({ showConfirm: vi.fn() }));
vi.mock('$lib/stores', async (importOriginal) => {
    const real = await importOriginal<typeof import('$lib/stores')>();
    return { ...real, dialogStore: { ...real.dialogStore, showConfirm, showToast: vi.fn() } };
});

import ExtensionsTab from '$lib/components/ConfigModal/ExtensionsTab.svelte';
import type { Extension } from '$lib/types';

/**
 * An update that can contact a new host is not applied until the user accepts it. `Update all` skips
 * such rows, and `Update` asks first and sends the answer with the request.
 */

const NEW_HOST_ID = 'search-1';
const PLAIN_ID = 'notes-1';
const NEW_HOST_NOTE = 'can contact a host the installed version was never granted';

function extension(id: string, name: string): Extension {
    return {
        id,
        name,
        author: 'tester',
        version: '1.0.0',
        type: 'ui',
        manifest: { display_name: name, description: `${name} extension`, capabilities: { ui: {} } },
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

interface Harness {
    /** Update operations, in order. */
    updates: { id: string; body: any }[];
}

function stubFetch(h: Harness) {
    const encoder = new TextEncoder();
    const sse = () => ({
        ok: true,
        status: 200,
        headers: new Headers({ 'Content-Type': 'text/event-stream' }),
        body: new ReadableStream<Uint8Array>({
            start(controller) {
                controller.enqueue(encoder.encode(
                    'data: ' + JSON.stringify({ extension: 'updated', progress: 100, message: 'done', done: true }) + '\n\n'));
                controller.close();
            },
        }),
    });
    const json = (body: unknown) =>
        new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } });

    vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
        const url = new URL(String(input), 'http://localhost');
        const method = init?.method || 'GET';

        if (url.pathname === '/api/auth/session') return json({ authenticated: true, csrf_token: 'csrf-test' });
        if (url.pathname === '/api/home') return json({ home: '/home/tester' });
        if (url.pathname === '/api/extensions/registry') return json([]);
        if (url.pathname === '/api/extensions/local') return json([]);
        if (url.pathname === '/api/extensions' && method === 'GET') {
            return json([extension(NEW_HOST_ID, 'web-search'), extension(PLAIN_ID, 'notes')]);
        }
        if (url.pathname === '/api/extensions/check-updates') {
            return json([
                // Reaches a new host, and asks for no new permission.
                { id: NEW_HOST_ID, name: 'web-search', current_version: '1.0.0', available_version: '2.0.0', domains_added: true },
                { id: PLAIN_ID, name: 'notes', current_version: '1.0.0', available_version: '2.0.0' },
            ]);
        }
        const update = url.pathname.match(/^\/api\/extensions\/([^/]+)\/update$/);
        if (update && method === 'POST') {
            h.updates.push({ id: decodeURIComponent(update[1]), body: JSON.parse(String(init?.body || '{}')) });
            return sse();
        }
        return json({ error: `unexpected request ${method} ${url.pathname}` });
    }));
}

async function renderWithUpdates() {
    const utils = render(ExtensionsTab);
    await waitFor(() => expect(utils.container.querySelector('.ext-name')).toBeTruthy());
    const check = [...utils.container.querySelectorAll('button')]
        .find((b) => b.textContent?.includes('Check for Updates'));
    await fireEvent.click(check!);
    await waitFor(() => expect(utils.container.querySelectorAll('.update-item').length).toBe(2));
    return utils;
}

function updateRow(container: HTMLElement, name: string): HTMLElement {
    const row = [...container.querySelectorAll<HTMLElement>('.update-item')]
        .find((r) => r.querySelector('.update-name')?.textContent?.trim() === name);
    if (!row) throw new Error(`no update row for ${name}`);
    return row;
}

function rowUpdateButton(row: HTMLElement): HTMLButtonElement {
    return row.querySelector('button.update-action') as HTMLButtonElement;
}

beforeEach(() => {
    showConfirm.mockReset();
});

afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
});

describe('an update that reaches a new host', () => {
    it('says so on its row, and does not flag the ordinary one', async () => {
        stubFetch({ updates: [] });
        const { container } = await renderWithUpdates();

        expect(updateRow(container, 'web-search').textContent).toContain(NEW_HOST_NOTE);
        expect(updateRow(container, 'notes').textContent).not.toContain(NEW_HOST_NOTE);
    });

    it('is left out of Update all, which reports what it skipped', async () => {
        const h: Harness = { updates: [] };
        stubFetch(h);
        const { container } = await renderWithUpdates();

        const updateAll = [...container.querySelectorAll('button')]
            .find((b) => b.textContent?.includes('Update All'));
        await fireEvent.click(updateAll!);

        await waitFor(() => expect(h.updates.length).toBe(1));
        expect(h.updates[0].id).toBe(PLAIN_ID);
        expect(h.updates[0].body).toEqual({ acknowledge_new_domains: false });
        expect(showConfirm).not.toHaveBeenCalled();
        await waitFor(() => expect(container.textContent).toContain('Left for you to decide, not updated: web-search'));
        // The row stays in the list: it still needs a decision.
        expect(updateRow(container, 'web-search')).toBeTruthy();
    });

    it('asks before updating, and a refusal sends no request', async () => {
        const h: Harness = { updates: [] };
        stubFetch(h);
        showConfirm.mockResolvedValue(false);
        const { container } = await renderWithUpdates();

        await fireEvent.click(rowUpdateButton(updateRow(container, 'web-search')));

        await waitFor(() => expect(showConfirm).toHaveBeenCalled());
        expect(showConfirm.mock.calls[0][0].title).toBe('This update reaches a new host');
        expect(h.updates).toEqual([]);
    });

    it('sends the review answer with the request when accepted', async () => {
        const h: Harness = { updates: [] };
        stubFetch(h);
        showConfirm.mockResolvedValue(true);
        const { container } = await renderWithUpdates();

        await fireEvent.click(rowUpdateButton(updateRow(container, 'web-search')));

        await waitFor(() => expect(h.updates.length).toBe(1));
        expect(h.updates[0]).toEqual({ id: NEW_HOST_ID, body: { acknowledge_new_domains: true } });
    });

    it('updates an ordinary version without asking', async () => {
        const h: Harness = { updates: [] };
        stubFetch(h);
        const { container } = await renderWithUpdates();

        await fireEvent.click(rowUpdateButton(updateRow(container, 'notes')));

        await waitFor(() => expect(h.updates.length).toBe(1));
        expect(showConfirm).not.toHaveBeenCalled();
    });
});
