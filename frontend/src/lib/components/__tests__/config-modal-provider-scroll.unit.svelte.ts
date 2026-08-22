import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from 'vitest';
import { render, fireEvent, cleanup, waitFor } from '@testing-library/svelte';
import ConfigModal from '$lib/components/ConfigModal.svelte';
import type { Config, Extension } from '$lib/types';
import { listExtensions } from '$lib/api/extensions';

// The mock factory is hoisted above module imports, so everything it needs
// must be defined inside the factory itself.
vi.mock('$lib/api/extensions', () => ({
    listExtensions: vi.fn(async (): Promise<Extension[]> => []),
}));

/** scrollIntoView calls recorded by the jsdom stub (jsdom doesn't implement it). */
const scrolled: Array<{ el: Element; options: ScrollIntoViewOptions | boolean | undefined }> = [];

beforeAll(() => {
    // Polyfill matchMedia for useIsMobile (desktop: never matches).
    vi.stubGlobal('matchMedia', (query: string) => ({
        matches: false,
        media: query,
        onchange: null,
        addEventListener: () => {},
        removeEventListener: () => {},
        addListener: () => {},
        removeListener: () => {},
        dispatchEvent: () => false,
    }));

    // Record scrollIntoView calls instead of scrolling.
    Object.defineProperty(Element.prototype, 'scrollIntoView', {
        configurable: true,
        value: function (this: Element, options?: ScrollIntoViewOptions | boolean) {
            scrolled.push({ el: this, options });
        },
    });
});

beforeEach(() => {
    // Deterministic default regardless of test order (restoreAllMocks does not
    // reset vi.fn implementations).
    vi.mocked(listExtensions).mockResolvedValue([]);
});

afterEach(() => {
    cleanup();
    scrolled.length = 0;
    vi.restoreAllMocks();
});

function makeConfig(): Config {
    return {
        workspace_dir: '/tmp/wt',
        database_path: '/tmp/db',
        version: '1.0.0',
        providers: [
            { id: 1, url: 'http://localhost:9999/v1', models: [{ name: 'model-a' }], concurrency: 1, max_requests_per_second: 0 },
        ],
        worker_provider: '1',
        worker_model: 'model-a',
        utility_provider: '1',
        utility_model: 'model-a',
    };
}

function makeAgentExtension(): Extension {
    return {
        id: 'ext-1',
        name: 'pi',
        version: '1.0.0',
        type: 'agent',
        manifest: {
            display_name: 'Pi',
            description: 'Minimal agent harness',
            author: 'Pi',
            version: '1.0.0',
            capabilities: {
                agent: {
                    wasm_module: 'pi.wasm',
                    sandbox_mount: { host_path: '/root/.pi/agent', container_path: '/root/.pi/agent', mode: 'rw' },
                },
            },
        },
        config: {},
        container_config: {},
        sandbox_config: { network_isolation: true, memory_limit: 0 },
        install_source: 'registry',
        enabled: true,
        running: false,
        installed_at: '2025-01-01T00:00:00Z',
        built_at: '2025-01-01T00:00:00Z',
    };
}

function renderModal() {
    return render(ConfigModal, {
        props: { config: makeConfig(), onClose: vi.fn(), onSaved: vi.fn() },
    });
}

/**
 * Let the mount-time $effect cycle settle: it rebuilds `providers` from the
 * config prop and kicks off the (mocked) agent list load, and re-runs when the
 * agents-loading flags flip. Until it settles, it would clobber providers
 * added by a click that lands in the same microtask window (in the browser it
 * settles in the first microtask, long before a user could click).
 */
async function settle() {
    await vi.waitFor(() => expect(listExtensions).toHaveBeenCalled(), { timeout: 1000 });
    await new Promise(r => setTimeout(r, 20));
}

describe('ConfigModal — reveal newly added provider card', () => {
    it('scrolls the new LLM provider card into view and focuses its URL input', async () => {
        const { container } = renderModal();
        await settle();

        const addBtn = container.querySelector('button[title="Add a new LLM provider"]');
        expect(addBtn).toBeTruthy();
        fireEvent.click(addBtn as HTMLButtonElement);

        // The reveal runs after a tick + the slide-settle wait (~250 ms).
        await waitFor(() => expect(scrolled).toHaveLength(1), { timeout: 1000 });
        // The scrolled element is the NEW card's URL input (index 1 — the new
        // provider appended after the seeded one), not the whole card.
        expect(scrolled[0].el.id).toBe('provider-url-1');
        expect(scrolled[0].options).toEqual({ block: 'nearest', behavior: 'smooth' });

        // The new card's URL input receives focus.
        const urlInput = document.getElementById('provider-url-1');
        expect(urlInput).toBeTruthy();
        expect(document.activeElement).toBe(urlInput);
    });

    it('scrolls the new agent card into view when adding an agent from the menu', async () => {
        vi.mocked(listExtensions).mockResolvedValue([makeAgentExtension()]);
        const { container } = renderModal();
        await settle();

        fireEvent.click(container.querySelector('button[title="Add a coding agent"]') as HTMLButtonElement);

        // The menu item appears once the (mocked) extension list resolves.
        const menuItem = await waitFor(() => {
            const item = container.querySelector('.agent-menu-item');
            expect(item).toBeTruthy();
            return item;
        }, { timeout: 1000 });
        expect(menuItem).toBeTruthy();
        fireEvent.click(menuItem!);

        await waitFor(() => expect(scrolled).toHaveLength(1), { timeout: 1000 });
        expect(scrolled[0].el.getAttribute('data-index')).toBe('1');
        expect(scrolled[0].options).toEqual({ block: 'nearest', behavior: 'smooth' });

        // Agent cards have no inputs — focus must not land inside a provider card.
        expect(document.activeElement?.closest('.provider-card') ?? null).toBeNull();
    });
});
