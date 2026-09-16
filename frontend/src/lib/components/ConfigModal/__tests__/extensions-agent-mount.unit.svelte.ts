import { describe, it, expect, vi, afterEach, beforeAll } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import ExtensionsTab from '$lib/components/ConfigModal/ExtensionsTab.svelte';
import type { Extension, RegistryEntry } from '$lib/types';

/**
 * Tests for the agent's config folder: the Browse install dialog asks the user about it, the card shows
 * whether it was approved, and the Edit dialog saves it through its own endpoint.
 */

const PI_ID = 'pi-coding-agent-pi';
const BLAND_ID = 'acme-bland';
const PROPOSAL = { host_path: '~/.pi/agent', container_path: '/root/.pi/agent', mode: 'rw' };

function agentCapabilities(mounts: object[] | undefined) {
    return { agent: { wasm_module: 'pi.wasm', ...(mounts ? { sandbox: { mount: mounts } } : {}) } };
}

/**
 * An installed agent as the extension endpoint reports it. `stored` is rewritten into the field order the
 * server uses, because the dialog must not report a change just because the order differs.
 */
function agentExtension(id: string, name: string, capabilities: object, stored?: object[], enabled = true): Extension {
    const config: Record<string, any> = {};
    if (stored) {
        config.sandbox_mount = JSON.parse(JSON.stringify(stored.map((m) => {
            const mount = m as Record<string, string>;
            return { container_path: mount.container_path, host_path: mount.host_path, mode: mount.mode };
        })));
    }
    return {
        id,
        name,
        // The install uses the entry's own author name, so the Browse entry and the installed card match.
        author: id === PI_ID ? 'Pi Coding Agent' : 'Acme',
        version: '1.0.0',
        type: 'agent',
        manifest: {
            display_name: name,
            description: `${name} coding agent`,
            capabilities,
            // The shipped schema still declares `sandbox_mount`, which the app itself saves.
            config_schema: {
                properties: [
                    { key: 'sandbox_mount', type: 'mount', title: 'Sandbox Mounts', default: [PROPOSAL] },
                    { key: 'sandbox_env', type: 'env', title: 'Environment Variables', default: [] },
                ],
            },
        },
        config,
        container_config: {},
        sandbox_config: {},
        install_source: 'registry',
        enabled,
        running: false,
        installed_at: '2026-01-01T00:00:00Z',
        built_at: '',
    } as unknown as Extension;
}

function registryEntry(name: string, capabilities: object): RegistryEntry {
    return {
        name,
        author: 'Pi Coding Agent',
        version: '1.0.0',
        type: 'agent',
        display_name: name,
        description: `${name} coding agent`,
        capabilities,
    } as unknown as RegistryEntry;
}

interface Harness {
    /** id -> the folders the server has saved as approved; no entry means nothing was approved. */
    granted: Record<string, object[] | undefined>;
    /** Which extensions are installed; approving a folder before an install has nothing to attach to, as on
     *  the server. */
    installed: string[];
    registry: RegistryEntry[];
    /** PUT /api/extensions/{id}/agent-mount calls, in order. */
    mounts: { id: string; body: any }[];
    /** PUT /api/extensions/{id} calls: the rest of what the dialog saves. */
    saves: { id: string; body: any }[];
    installs: string[];
    /** When set, the next folder request fails with this message. */
    mountFailure: string | null;
}

function newHarness(): Harness {
    return { granted: {}, installed: [PI_ID, BLAND_ID], registry: [], mounts: [], saves: [], installs: [], mountFailure: null };
}

/** A canned install stream: progress events, then the final event that a folder approval waits for. */
function installStream(name: string): Response {
    const body = new ReadableStream<Uint8Array>({
        start(controller) {
            const send = (event: object) =>
                controller.enqueue(new TextEncoder().encode(`data: ${JSON.stringify(event)}\n\n`));
            send({ extension: name, progress: 0.5, message: 'Cloning', done: false });
            send({ extension: name, progress: 1, message: 'Installed', done: true });
            controller.close();
        },
    });
    return new Response(body, { status: 200, headers: { 'Content-Type': 'text/event-stream' } });
}

function stubFetch(h: Harness) {
    vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
        const url = new URL(String(input), 'http://localhost');
        const json = (body: unknown, status = 200) =>
            new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

        if (url.pathname === '/api/auth/session') return json({ authenticated: true, csrf_token: 'csrf-test' });
        if (url.pathname === '/api/home') return json({ home: '/home/tester' });
        if (url.pathname === '/api/extensions/registry') return json(h.registry);
        if (url.pathname === '/api/extensions/local') return json([]);
        if (url.pathname === '/api/extensions/check-updates') return json([]);

        if (url.pathname === '/api/extensions' && (!init?.method || init.method === 'GET')) {
            const rows = [
                agentExtension(PI_ID, 'pi', agentCapabilities([PROPOSAL]), h.granted[PI_ID]),
        
                agentExtension(BLAND_ID, 'bland', agentCapabilities(undefined), undefined),
            ];
            return json(rows.filter((r) => h.installed.includes(r.id)));
        }

        if (url.pathname === '/api/extensions/install' && init?.method === 'POST') {
            const name = JSON.parse(String(init.body || '{}')).name;
            h.installs.push(name);
            if (!h.installed.includes(PI_ID)) h.installed.push(PI_ID);
            return installStream(name);
        }

        const grant = url.pathname.match(/^\/api\/extensions\/([^/]+)\/agent-mount$/);
        if (grant && init?.method === 'PUT') {
            const id = decodeURIComponent(grant[1]);
            const body = JSON.parse(String(init.body || '{}'));
            h.mounts.push({ id, body });
            if (h.mountFailure) return json({ error: h.mountFailure }, 400);
            h.granted[id] = body.mounts.length ? body.mounts : undefined;
            // The endpoint answers with the extension, and the open dialog reads the saved list back from it.
            return json(agentExtension(id, id === PI_ID ? 'pi' : 'bland', agentCapabilities([PROPOSAL]), h.granted[id]));
        }

        const save = url.pathname.match(/^\/api\/extensions\/([^/]+)$/);
        if (save && (init?.method === 'PUT' || init?.method === 'PATCH')) {
            h.saves.push({ id: decodeURIComponent(save[1]), body: JSON.parse(String(init.body || '{}')) });
            return json({ ok: true });
        }

        return json({ error: `unexpected request ${init?.method || 'GET'} ${url.pathname}` }, 404);
    }));
}

async function renderTab() {
    const utils = render(ExtensionsTab, { props: { onExtensionsChanged: () => {} } });
    await waitFor(() => {
        expect(utils.container.querySelector('.ext-name')).toBeTruthy();
    });
    return utils;
}

function card(container: HTMLElement, name: string): HTMLElement | undefined {
    return [...container.querySelectorAll<HTMLElement>('.extension-card')]
        .find((c) => c.querySelector('.ext-name')?.textContent?.trim() === name);
}

function cardButtons(container: HTMLElement, name: string): HTMLButtonElement[] {
    return [...(card(container, name)?.querySelectorAll('button') ?? [])] as HTMLButtonElement[];
}

function buttonsIn(root: Element | undefined | null): HTMLButtonElement[] {
    return [...(root?.querySelectorAll('button') ?? [])] as HTMLButtonElement[];
}

function buttonByText(buttons: HTMLButtonElement[], text: string): HTMLButtonElement | undefined {
    return buttons.find((b) => b.textContent?.trim() === text);
}

async function openBrowse(container: HTMLElement) {
    const tab = [...container.querySelectorAll('.view-tab')].find((b) => b.textContent?.includes('Browse'));
    if (!tab) throw new Error('the Extensions tab has no Browse view');
    await fireEvent.click(tab);
    await waitFor(() => expect(container.querySelector('.registry-card')).toBeTruthy());
}

async function clickByText(container: HTMLElement, text: string) {
    const button = buttonByText([...container.querySelectorAll('button')] as HTMLButtonElement[], text);
    if (!button) throw new Error(`nothing in the dialog is labelled "${text}"`);
    await fireEvent.click(button);
}

/** Opens the extension's Edit dialog, clicking its "Needs setup" badge before setup and its Edit icon
 *  afterwards. */
async function openEditFor(container: HTMLElement, name: string) {
    const buttons = cardButtons(container, name);
    const target = buttonByText(buttons, 'Needs setup')
        ?? buttons.find((b) => b.title.startsWith('Edit') && !b.querySelector('.ext-needs-dot'));
    if (!target) throw new Error(`no way to edit ${name}`);
    await fireEvent.click(target);
    await waitFor(() => expect(container.querySelector('#edit-agent-mount-0')).toBeTruthy());
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

describe('the installed card: who is still without a folder', () => {
    it('badges the agent that proposed a folder and was granted none', async () => {
        const h = newHarness();
        stubFetch(h);
        const { container } = await renderTab();

        const pi = cardButtons(container, 'pi');
        expect(buttonByText(pi, 'Needs setup')).toBeTruthy();
        expect(pi.some((b) => b.title.startsWith('Edit') && b.querySelector('.ext-needs-dot'))).toBe(true);
        expect(card(container, 'pi')?.textContent).toContain('No configuration folder granted');

        const bland = cardButtons(container, 'bland');
        expect(buttonByText(bland, 'Needs setup')).toBeUndefined();
        expect(bland.some((b) => b.querySelector('.ext-needs-dot'))).toBe(false);
    });

    it('shows the granted folder with the way to take it back', async () => {
        const h = newHarness();
        h.granted[PI_ID] = [{ host_path: '/home/tester/.pi/agent', container_path: '/root/.pi/agent', mode: 'rw' }];
        stubFetch(h);
        const { container } = await renderTab();

        expect(buttonByText(cardButtons(container, 'pi'), 'Needs setup')).toBeUndefined();
        const row = card(container, 'pi')?.querySelector('.ext-agent-path');
        expect(row?.textContent).toContain('Mounted rw');
        expect(row?.querySelector('code')?.textContent).toBe('/home/tester/.pi/agent');

        await fireEvent.click(buttonByText(cardButtons(container, 'pi'), 'Unmount')!);
        await waitFor(() => expect(h.mounts).toEqual([{ id: PI_ID, body: { mounts: [] } }]));

        await waitFor(() => expect(buttonByText(cardButtons(container, 'pi'), 'Needs setup')).toBeTruthy());
    });
});

describe('Browse: the question is asked before anything is installed', () => {
    it('reviews the mount of an entry whose index carries the proposal', async () => {
        const h = newHarness();
        h.installed = [BLAND_ID];
        h.registry = [registryEntry('pi', agentCapabilities([PROPOSAL]))];
        stubFetch(h);
        const { container } = await renderTab();
        await openBrowse(container);

        expect(card(container, 'pi')?.querySelector('.ext-needs-setup-note')?.textContent)
            .toContain('/root/.pi/agent');

        await fireEvent.click(buttonByText(buttonsIn(card(container, 'pi')), 'Install')!);

        await waitFor(() => expect(container.textContent).toContain('Grant it a configuration folder?'));
        expect(container.textContent).toContain('/root/.pi/agent');
        expect(container.textContent).toContain('Suggested: ~/.pi/agent → /root/.pi/agent (rw)');

        await clickByText(container, 'Don’t mount');
        await waitFor(() => expect(h.installs).toEqual(['pi']));
        // "Don't mount" is an answer too: install, and approve no folder.
        expect(h.mounts).toEqual([]);
    });

    it('grants the proposed folder to the row the install just created', async () => {
        const h = newHarness();
        h.installed = [BLAND_ID];
        h.registry = [registryEntry('pi', agentCapabilities([PROPOSAL]))];
        stubFetch(h);
        const { container } = await renderTab();
        await openBrowse(container);
        await fireEvent.click(buttonByText(buttonsIn(card(container, 'pi')), 'Install')!);
        await waitFor(() => expect(container.textContent).toContain('Grant it a configuration folder?'));

        await clickByText(container, 'Mount');

        // The approval is sent after the stream's last event, so the extension already exists.
        await waitFor(() => expect(h.mounts).toEqual([{
            id: PI_ID,
            body: { mounts: [PROPOSAL] },
        }]));
    });

    it('installs nothing when the dialog is dismissed', async () => {
        const h = newHarness();
        h.installed = [BLAND_ID];
        h.registry = [registryEntry('pi', agentCapabilities([PROPOSAL]))];
        stubFetch(h);
        const { container } = await renderTab();
        await openBrowse(container);
        await fireEvent.click(buttonByText(buttonsIn(card(container, 'pi')), 'Install')!);
        await waitFor(() => expect(container.textContent).toContain('Grant it a configuration folder?'));

        await clickByText(container, 'Cancel');

        await waitFor(() => expect(container.textContent).not.toContain('Grant it a configuration folder?'));
        expect(h.installs).toEqual([]);
        expect(h.mounts).toEqual([]);
    });
});

describe('the Edit dialog: the folder, beside the rest of the settings', () => {
    it('renders one row per proposed folder and no second editor for the grant', async () => {
        const h = newHarness();
        stubFetch(h);
        const { container } = await renderTab();
        await openEditFor(container, 'pi');

        const input = container.querySelector('#edit-agent-mount-0') as HTMLInputElement;
        // The suggested path is only placeholder text; the field itself starts empty.
        expect(input.value).toBe('');
        expect(input.getAttribute('placeholder')).toBe('~/.pi/agent');
        expect(input.getAttribute('aria-label')).toContain('pi');
        // Only one place writes the approval, so the schema's own mount field renders nothing.
        expect([...container.querySelectorAll('.field-label')].map((l) => l.textContent)).not.toContain('Sandbox Mounts');
        expect(h.mounts).toEqual([]);
    });

    it('sends the folder with its Save and stops warning once it is granted', async () => {
        const h = newHarness();
        stubFetch(h);
        const { container } = await renderTab();
        await openEditFor(container, 'pi');

        const input = container.querySelector('#edit-agent-mount-0') as HTMLInputElement;
        input.value = '/data/pi/profile';
        input.dispatchEvent(new Event('input', { bubbles: true }));
        await clickByText(container, 'Save');

        await waitFor(() => expect(h.mounts).toEqual([{
            id: PI_ID,
            body: { mounts: [{ host_path: '/data/pi/profile', container_path: '/root/.pi/agent', mode: 'rw' }] },
        }]));
        await waitFor(() => expect(h.saves).toHaveLength(1));

        expect(h.saves[0].body.config.sandbox_mount).toBeUndefined();
        await waitFor(() => expect(buttonByText(cardButtons(container, 'pi'), 'Needs setup')).toBeUndefined());
    });

    it('sends no grant request when the rows still show the stored folders', async () => {
        // Saving an unrelated setting must not re-send a folder list that did not change, even though the
        // server writes the fields in a different order from this form.
        const h = newHarness();
        h.granted[PI_ID] = [{ host_path: '/home/tester/.pi/agent', container_path: '/root/.pi/agent', mode: 'rw' }];
        stubFetch(h);
        const { container } = await renderTab();
        await openEditFor(container, 'pi');

        await clickByText(container, 'Save');

        await waitFor(() => expect(h.saves).toHaveLength(1));
        expect(h.mounts).toEqual([]);
    });

    it('keeps the dialog open on a refusal and puts the server’s sentence on the folder', async () => {
        const h = newHarness();
        h.mountFailure = '/home/tester/.ssh is a home directory, not a directory to mount';
        stubFetch(h);
        const { container } = await renderTab();
        await openEditFor(container, 'pi');

        const input = container.querySelector('#edit-agent-mount-0') as HTMLInputElement;
        input.value = '/home/tester/.ssh';
        input.dispatchEvent(new Event('input', { bubbles: true }));
        await clickByText(container, 'Save');

        await waitFor(() => expect(container.querySelector('.ext-dep-error')?.textContent).toContain('home directory'));

        expect(container.querySelector('#edit-agent-mount-0')).toBeTruthy();
        expect(h.saves).toEqual([]);
    });
});
