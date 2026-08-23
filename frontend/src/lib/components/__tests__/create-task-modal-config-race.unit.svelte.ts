import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render, cleanup, waitFor } from '@testing-library/svelte';
import CreateTaskModal from '$lib/components/CreateTaskModal.svelte';
import { appStore } from '$lib/stores';
import type { Config } from '$lib/types';

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
});

function makeConfig(): Config {
    return {
        workspace_dir: '/tmp/wt',
        database_path: '/tmp/db',
        version: '1.0.0',
        providers: [
            { id: 1, url: 'http://llm:8080/v1', models: [{ name: 'model-a' }], concurrency: 1, max_requests_per_second: 0 },
            { name: 'pi', kind: 'agent', url: 'agent://pi', display_name: 'Pi Coding Agent', concurrency: 1, max_requests_per_second: 0, models: [] },
        ],
        worker_provider: '1',
        worker_model: 'model-a',
        utility_provider: '1',
        utility_model: 'model-a',
    };
}

const props = { projectId: 'p1', onClose: () => {} };

describe('CreateTaskModal — provider row reactivity (config arrival vs modal mount)', () => {
    afterEach(() => {
        cleanup();
        appStore.config = null;
    });

    it('renders the model row when config is set before mount', async () => {
        appStore.config = makeConfig();
        const { container } = render(CreateTaskModal, { props });
        await waitFor(() => expect(container.querySelector('.model-select-row')).toBeTruthy(), { timeout: 2000 });
    });

    it('renders the model row when config arrives after mount', async () => {
        appStore.config = null;
        const { container } = render(CreateTaskModal, { props });
        // Let the initial render + mount effects settle.
        await new Promise(r => setTimeout(r, 50));
        expect(container.querySelector('.model-select-row')).toBeNull();
        // Config arrives (app's getConfig() resolving after the modal opened).
        appStore.config = makeConfig();
        await waitFor(() => expect(container.querySelector('.model-select-row')).toBeTruthy(), { timeout: 2000 });
    });
});
