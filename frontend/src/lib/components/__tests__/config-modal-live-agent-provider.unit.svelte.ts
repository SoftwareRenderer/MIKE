import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import ConfigModal from '$lib/components/ConfigModal.svelte';
import type { Config, Provider } from '$lib/types';

// The point of this file: the dialog is OPEN when the server starts (or stops) reporting an
// agent provider — the user is on the Extensions tab installing it, and the app re-reads the
// config on the server's extension_change broadcast. The derived agent row must appear
// without a page reload, and the LLM card being edited beside it must survive.

vi.mock('svelte/transition', () => ({
    slide: vi.fn(),
    fade: vi.fn(),
}));

beforeAll(() => {
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
    // jsdom has no scrollIntoView; "+ Add Provider" scrolls the new card into view.
    Object.defineProperty(Element.prototype, 'scrollIntoView', {
        configurable: true,
        value: vi.fn(),
    });
});

afterEach(() => cleanup());

const llm: Provider = { id: 1, url: 'http://localhost:9999/v1', models: [{ name: 'model-a' }], concurrency: 1, max_requests_per_second: 0 };
const pi: Provider = { name: 'pi', kind: 'agent', url: 'agent://pi', display_name: 'Pi Coding Agent', concurrency: 1, max_requests_per_second: 0, models: [] };

function makeConfig(providers: Provider[]): Config {
    return {
        workspace_dir: '/tmp/wt',
        database_path: '/tmp/db',
        version: '1.0.0',
        providers,
        worker_provider: '1',
        worker_model: 'model-a',
        utility_provider: '1',
        utility_model: 'model-a',
    };
}

/** Let the mount-time $effect cycle settle (it builds the form from the config prop). */
async function settle() {
    await new Promise(r => setTimeout(r, 20));
}

describe('ConfigModal — agent provider appearing while the dialog is open', () => {
    it('shows a newly installed agent without discarding the edit in progress', async () => {
        const { container, rerender } = render(ConfigModal, {
            props: { config: makeConfig([llm]), onClose: vi.fn(), onSaved: vi.fn() },
        });
        await settle();
        expect(container.querySelector('.agent-icon')).toBeNull();

        // Work in progress the server knows nothing about: a provider the user added and has
        // not saved. The config prop still describes only the seeded provider, so anything
        // that rebuilds the list from it throws this card away.
        fireEvent.click(container.querySelector('button[title="Add a new LLM provider"]') as HTMLButtonElement);
        await new Promise(r => setTimeout(r, 400)); // the reveal waits for the card to settle
        expect(container.querySelectorAll('.provider-card').length).toBe(2);

        // The agent extension is installed: GET /api/config now reports agent://pi.
        rerender({ config: makeConfig([llm, pi]) });

        await waitFor(() => expect(container.querySelector('.agent-icon')).toBeTruthy());
        // Named from the manifest, not the URL.
        expect(container.textContent).toContain('Pi Coding Agent');
        // ...and selectable as the worker/utility default.
        const workerSelect = container.querySelector('select[aria-label="Worker provider"]') as HTMLSelectElement;
        expect(Array.from(workerSelect.options).some(o => o.textContent === 'Pi Coding Agent')).toBe(true);
        // The unsaved card is still there, ahead of the derived row.
        expect(container.querySelectorAll('.provider-card').length).toBe(3);
    });

    it('keeps an unsaved change to the form fields beside the agent row', async () => {
        const { container, rerender } = render(ConfigModal, {
            props: { config: makeConfig([llm]), onClose: vi.fn(), onSaved: vi.fn() },
        });
        await settle();

        // An unsaved change to a form field the user owns.
        const workerSelect = container.querySelector('select[aria-label="Worker model"]') as HTMLSelectElement;
        fireEvent.change(workerSelect, { target: { value: 'model-a' } });

        rerender({ config: makeConfig([llm, pi]) });
        await waitFor(() => expect(container.querySelector('.agent-icon')).toBeTruthy());

        const after = container.querySelector('select[aria-label="Worker model"]') as HTMLSelectElement;
        expect(after.value).toBe('model-a');
    });

    it('drops the agent row when the server stops reporting it', async () => {
        const { container, rerender } = render(ConfigModal, {
            props: { config: makeConfig([llm, pi]), onClose: vi.fn(), onSaved: vi.fn() },
        });
        await settle();
        expect(container.querySelector('.agent-icon')).toBeTruthy();

        // The extension was disabled or uninstalled.
        rerender({ config: makeConfig([llm]) });

        await waitFor(() => expect(container.querySelector('.agent-icon')).toBeNull());
        expect(container.textContent).not.toContain('Pi Coding Agent');
        // The provider the user owns is untouched.
        expect((container.querySelector('.providers-list') as HTMLElement).textContent).toContain('localhost:9999');
    });
});
