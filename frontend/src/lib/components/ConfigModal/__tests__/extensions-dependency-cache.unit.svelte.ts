import { describe, it, expect, vi, afterEach, beforeAll } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import ExtensionsTab from '$lib/components/ConfigModal/ExtensionsTab.svelte';
import type { Extension, RegistryEntry } from '$lib/types';

/**
 * The dependency directory: the card says who still needs one (a badge and a dot on Edit, both
 * `needsDependencyGrant`), and the configuration itself lives in the Edit dialog and rides its Save —
 * so a path is sent only when the field differs from what is stored, Cancel sends nothing, and a
 * refusal lands on the field it is about with the dialog still open.
 *
 * The Browse card carries the pre-install half: the badge and the note that say installation is only
 * the first step.
 */

const HOME = '/home/tester';
const GO_ID = 'go-team-gopls';
const TS_ID = 'microsoft-typescript';

/** The gopls shape as the server reports it: declaration present, grant state field absent until the
 *  user names a directory. */
function lspExtension(id: string, name: string, sandbox: Extension['sandbox_config'], enabled = true): Extension {
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
        enabled,
        running: false,
        installed_at: '2026-01-01T00:00:00Z',
        built_at: '',
    } as unknown as Extension;
}

const DECLARED = { container_path: '/go/pkg/mod', env: 'GOMODCACHE' };

/** A registry index entry, where the declaration sits on `lsp`. */
function registryEntry(name: string, author: string, capabilities: Record<string, any>): RegistryEntry {
    return {
        name,
        author,
        version: '0.7.0',
        type: 'lsp',
        display_name: name,
        description: `${name} language server`,
        capabilities,
    } as unknown as RegistryEntry;
}

/** A local entry as `GET /api/extensions/local` answers it: the manifest as written, with the
 *  declaration under `lsp.sandbox`. */
function localEntry(name: string, author: string, capabilities: Record<string, any>): RegistryEntry {
    return { ...registryEntry(name, author, capabilities), local_path: `/opt/extensions/${author}/${name}` };
}

interface Harness {
    /** What the store holds; the list endpoint answers from this, so a granted path shows up after reload. */
    granted: Record<string, string>;
    /** Whether the declarer is switched on — a disabled extension must not be warned about. */
    enabled: boolean;
    registry: RegistryEntry[];
    local: RegistryEntry[];
    /** Grant calls, in order: the grant endpoint has its own route precisely because empty means revoke. */
    puts: { id: string; body: any }[];
    /** The dialog's ordinary save (`PUT /api/extensions/{id}`), which the grant now rides alongside. */
    saves: { id: string; body: any }[];
    /** Set to make the next grant PUT fail with this message (the server's sentence, not a code). */
    putFailure: string | null;
}

function stubFetch(h: Harness) {
    vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
        const url = new URL(String(input), 'http://localhost');
        const json = (body: unknown, status = 200) =>
            new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

        if (url.pathname === '/api/auth/session') return json({ authenticated: true, csrf_token: 'csrf-test' });
        if (url.pathname === '/api/home') return json({ home: HOME });
        if (url.pathname === '/api/extensions/registry') return json(h.registry);
        if (url.pathname === '/api/extensions/local') return json(h.local);

        if (url.pathname === '/api/extensions' && (!init?.method || init.method === 'GET')) {
            return json([
                lspExtension(GO_ID, 'gopls', {
                    network_isolation: true,
                    memory_limit: 0,
                    dependency_cache: DECLARED,
                    dependency_host_path: h.granted[GO_ID] || '',
                }, h.enabled),
                // A server whose dependencies live in the checkout: it declares nothing, so it must
                // not be offered a directory it would never read.
                lspExtension(TS_ID, 'typescript', { network_isolation: true, memory_limit: 0 }),
            ]);
        }

        const grant = url.pathname.match(/^\/api\/extensions\/([^/]+)\/dependency-cache$/);
        if (grant && (init?.method === 'PUT' || init?.method === 'PATCH')) {
            const body = JSON.parse(String(init.body || '{}'));
            h.puts.push({ id: decodeURIComponent(grant[1]), body });
            if (h.putFailure) return json({ error: h.putFailure }, 400);
            h.granted[decodeURIComponent(grant[1])] = (body.host_path || '').trim();
            return json({ ok: true });
        }

        const save = url.pathname.match(/^\/api\/extensions\/([^/]+)$/);
        if (save && (init?.method === 'PUT' || init?.method === 'PATCH')) {
            h.saves.push({ id: decodeURIComponent(save[1]), body: JSON.parse(String(init.body || '{}')) });
            return json({ ok: true });
        }

        return json({ error: `unexpected request ${init?.method || 'GET'} ${url.pathname}` }, 404);
    }));
}

function newHarness(): Harness {
    return { granted: {}, enabled: true, registry: [], local: [], puts: [], saves: [], putFailure: null };
}

async function renderTab() {
    const utils = render(ExtensionsTab);
    await waitFor(() => {
        expect(utils.container.querySelector('.ext-name')).toBeTruthy();
    });
    return utils;
}

/** One extension's card, by the name in its title row. */
function card(container: HTMLElement, name: string): HTMLElement | undefined {
    const cards = [...container.querySelectorAll<HTMLElement>('.extension-card')];
    return cards.find((c) => c.querySelector('.ext-name')?.textContent?.trim() === name);
}

/** Every button of one extension's card, by the name shown in its title row. */
function cardButtons(container: HTMLElement, name: string): HTMLButtonElement[] {
    return [...(card(container, name)?.querySelectorAll('button') ?? [])] as HTMLButtonElement[];
}

/** Switch to the Browse view and wait for its cards. */
async function openBrowse(container: HTMLElement) {
    const tab = [...container.querySelectorAll('.view-tab')].find((b) => b.textContent?.includes('Browse'));
    if (!tab) throw new Error('the Extensions tab has no Browse view');
    await fireEvent.click(tab);
    // Wait for a card: the "no extensions" empty state would otherwise pass as the loaded list.
    await waitFor(() => expect(container.querySelector('.registry-card')).toBeTruthy());
}

function buttonByText(buttons: HTMLButtonElement[], text: string): HTMLButtonElement | undefined {
    return buttons.find((b) => b.textContent?.trim() === text);
}

/** Open the Edit dialog of one extension the way a person does: its badge, or the Edit icon. */
async function openEditFor(container: HTMLElement, name: string) {
    const badge = buttonByText(cardButtons(container, name), 'Needs setup');
    const target = badge ?? cardButtons(container, name).find((b) => b.title.startsWith('Edit'));
    if (!target) throw new Error(`no way to edit ${name}`);
    await fireEvent.click(target);
    await waitFor(() => expect(container.querySelector('#edit-dep-path')).toBeTruthy());
}

async function clickSave(container: HTMLElement) {
    const save = buttonByText([...container.querySelectorAll('button')] as HTMLButtonElement[], 'Save');
    if (!save) throw new Error('the dialog has no Save button');
    await fireEvent.click(save);
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

describe('Settings → Extensions: saying who needs a dependency directory', () => {
    it('warns only the extension that declared a cache and has no directory', async () => {
        const h = newHarness();
        stubFetch(h);
        const { container } = await renderTab();

        const gopls = cardButtons(container, 'gopls');
        expect(buttonByText(gopls, 'Needs setup')).toBeTruthy();
        expect(gopls.some((b) => b.title.startsWith('Edit') && b.querySelector('.ext-needs-dot'))).toBe(true);

        // A server whose dependencies live in the checkout declares nothing, so it is never warned about.
        const tsserver = cardButtons(container, 'typescript');
        expect(buttonByText(tsserver, 'Needs setup')).toBeUndefined();
        expect(tsserver.some((b) => b.querySelector('.ext-needs-dot'))).toBe(false);
    });

    it('says nothing about a directory that is already shared', async () => {
        const h = newHarness();
        h.granted[GO_ID] = '/data/shared/gomodcache';
        stubFetch(h);
        const { container } = await renderTab();

        const gopls = cardButtons(container, 'gopls');
        expect(buttonByText(gopls, 'Needs setup')).toBeUndefined();
        expect(gopls.some((b) => b.querySelector('.ext-needs-dot'))).toBe(false);
    });

    it('says nothing about an extension that is switched off', async () => {
        // It cannot run, so it is not waiting on anyone — and a warning about a thing that cannot
        // happen teaches people to ignore warnings.
        const h = newHarness();
        h.enabled = false;
        stubFetch(h);
        const { container } = await renderTab();

        const gopls = cardButtons(container, 'gopls');
        expect(buttonByText(gopls, 'Needs setup')).toBeUndefined();
        expect(gopls.some((b) => b.querySelector('.ext-needs-dot'))).toBe(false);
    });

    it('takes the badge to the dialog that states the declared location', async () => {
        const h = newHarness();
        stubFetch(h);
        const { container } = await renderTab();

        await openEditFor(container, 'gopls');

        // Named by its own declaration, so two extensions can never be confused for each other, and
        // the location inside the sandbox is stated because that half belongs to the manifest.
        const input = container.querySelector('#edit-dep-path') as HTMLInputElement;
        expect(input.getAttribute('aria-label') || '').toContain('gopls');
        expect(container.querySelector('.config-fields h4')?.textContent).toBe('Dependencies');
        expect(container.querySelector('.field-desc')?.textContent).toContain('GOMODCACHE=/go/pkg/mod');
    });
});

describe('Settings → Extensions: saying what an install will need afterwards', () => {
    it('badges a registry entry that declares a dependency directory, and says what for', async () => {
        const h = newHarness();
        h.registry = [
            registryEntry('gopls', 'Go Team', { lsp: { language: 'go', dependency_cache: DECLARED } }),
            registryEntry('typescript', 'Microsoft', { lsp: { language: 'typescript' } }),
        ];
        stubFetch(h);
        const { container } = await renderTab();
        await openBrowse(container);

        const gopls = card(container, 'gopls');
        const badge = gopls?.querySelector('.needs-setup-badge');
        expect(badge?.textContent?.trim()).toBe('Needs setup');
        // A label, not a button: nothing is installed yet, so there is no record to open.
        expect(badge?.tagName).toBe('SPAN');
        expect(badge?.getAttribute('title')).toContain('GOMODCACHE=/go/pkg/mod');

        const note = gopls?.querySelector('.ext-needs-setup-note');
        expect(note?.textContent).toContain('Requires additional setup after installation');
        expect(note?.textContent).toContain('GOMODCACHE=/go/pkg/mod');
        expect(note?.textContent).toContain('only what its own image contains');

        // An extension that declares nothing gets neither badge nor note.
        const tsserver = card(container, 'typescript');
        expect(tsserver?.querySelector('.needs-setup-badge')).toBeNull();
        expect(tsserver?.querySelector('.ext-needs-setup-note')).toBeNull();
    });

    it('says it for a local extension, whose declaration is in the manifest shape', async () => {
        // The local endpoint returns the manifest as written, so the declaration is under lsp.sandbox.
        const h = newHarness();
        h.local = [localEntry('dep-cache-lsp', 'Test Dev', {
            lsp: { language: 'go', sandbox: { dependency_cache: { container_path: '/fake/mod', env: 'FAKE_CACHE' } } },
        })];
        stubFetch(h);
        const { container } = await renderTab();
        await openBrowse(container);

        const fixture = card(container, 'dep-cache-lsp');
        expect(fixture?.querySelector('.needs-setup-badge')?.textContent?.trim()).toBe('Needs setup');
        expect(fixture?.querySelector('.ext-needs-setup-note')?.textContent).toContain('FAKE_CACHE=/fake/mod');
    });

    it('says it in Browse even once the extension is installed and configured', async () => {
        // Browse says what installing costs, which a folder on this machine does not change; the
        // Installed card is the one that stops warning once a folder is shared.
        const h = newHarness();
        h.granted[GO_ID] = '/data/shared/gomodcache';
        h.registry = [registryEntry('gopls', 'Go Team', { lsp: { language: 'go', dependency_cache: DECLARED } })];
        stubFetch(h);
        const { container } = await renderTab();

        expect(buttonByText(cardButtons(container, 'gopls'), 'Needs setup')).toBeUndefined();

        await openBrowse(container);
        const browseCard = card(container, 'gopls');
        expect(browseCard?.querySelector('.needs-setup-badge')?.textContent?.trim()).toBe('Needs setup');
        expect(browseCard?.textContent).toContain('Installed');
    });
});

describe('Settings → Extensions: the dialog that names the directory', () => {
    it('prefills the suggestion as editable text, and grants nothing by it', async () => {
        const h = newHarness();
        stubFetch(h);
        const { container } = await renderTab();
        await openEditFor(container, 'gopls');

        const input = container.querySelector('#edit-dep-path') as HTMLInputElement;
        // The suggestion arrives on the far side of two fetches (the list, then the home directory),
        // so it is waited for rather than assumed — and it arrives as text in the field, where it can
        // be read and edited, not as a value waiting to be applied.
        await waitFor(() => expect(input.value).toBe(`${HOME}/go/pkg/mod`));
        expect(container.querySelector('.ext-dep-none')).toBeTruthy();
        expect(h.puts).toEqual([]);
        // It is not a blank field either: the pending line says what saving would do.
        expect(container.querySelector('.ext-dep-pending')?.textContent).toContain('when you save');
    });

    it('grants the path in the field when the dialog saves, and stops warning afterwards', async () => {
        const h = newHarness();
        stubFetch(h);
        const { container } = await renderTab();
        await openEditFor(container, 'gopls');

        const input = container.querySelector('#edit-dep-path') as HTMLInputElement;
        input.value = '/data/shared/gomodcache';
        input.dispatchEvent(new Event('input', { bubbles: true }));
        await clickSave(container);

        await waitFor(() => expect(h.puts).toEqual([{ id: GO_ID, body: { host_path: '/data/shared/gomodcache' } }]));
        await waitFor(() => expect(h.saves).toHaveLength(1));
        // The dialog closes only once both calls are through, and the warning is gone because the
        // thing it warned about is done.
        await waitFor(() => expect(container.querySelector('#edit-dep-path')).toBeNull());
        expect(buttonByText(cardButtons(container, 'gopls'), 'Needs setup')).toBeUndefined();
    });

    it('revokes by clearing the field and saving — an empty path is a decision', async () => {
        const h = newHarness();
        h.granted[GO_ID] = '/data/shared/gomodcache';
        stubFetch(h);
        const { container } = await renderTab();
        await openEditFor(container, 'gopls');

        await waitFor(() => expect(container.querySelector('.ext-dep-path')?.textContent).toBe('/data/shared/gomodcache'));
        const clear = buttonByText([...container.querySelectorAll('button')] as HTMLButtonElement[], 'Unmount');
        if (!clear) throw new Error('a granted directory offers no way to unmount it');
        await fireEvent.click(clear);
        await clickSave(container);

        await waitFor(() => expect(h.puts).toHaveLength(1));
        expect(h.puts[0].body).toEqual({ host_path: '' });
        await waitFor(() => expect(buttonByText(cardButtons(container, 'gopls'), 'Needs setup')).toBeTruthy());
    });

    it('sends no grant request when the field still shows the stored path', async () => {
        // Save is one button for the whole dialog; the grant goes only when it actually changed, so
        // saving a memory limit cannot cost anyone a cold language server.
        const h = newHarness();
        h.granted[GO_ID] = '/data/shared/gomodcache';
        stubFetch(h);
        const { container } = await renderTab();
        await openEditFor(container, 'gopls');

        await clickSave(container);

        await waitFor(() => expect(h.saves).toHaveLength(1));
        expect(h.puts).toEqual([]);
    });

    it('keeps the dialog open on a refusal, and puts the server’s sentence on the field', async () => {
        const h = newHarness();
        h.putFailure = '/nope cannot be read (create it first — a task running the toolchain\'s download command is the usual way)';
        stubFetch(h);
        const { container } = await renderTab();
        await openEditFor(container, 'gopls');

        const input = container.querySelector('#edit-dep-path') as HTMLInputElement;
        input.value = '/nope';
        input.dispatchEvent(new Event('input', { bubbles: true }));
        await clickSave(container);

        await waitFor(() => expect(container.querySelector('.ext-dep-error')?.textContent).toContain('cannot be read'));
        // Still open, with nothing stored: the rest of the dialog is unsaved and unsaved is honest.
        expect(container.querySelector('#edit-dep-path')).toBeTruthy();
        expect(container.querySelector('.ext-dep-path')).toBeNull();
        expect(container.querySelector('.error-banner')).toBeNull();
        expect(h.saves).toEqual([]);
    });
});
