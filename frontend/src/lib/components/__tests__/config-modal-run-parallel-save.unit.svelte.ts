import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, fireEvent, cleanup, waitFor } from '@testing-library/svelte';
import ConfigModal from '$lib/components/ConfigModal.svelte';
import type { Config } from '$lib/types';
import { updateConfig } from '$lib/api';

// Only stub updateConfig; preserve the rest (getConfig/fetchModels/testConnection)
// so ConfigModal's mount behavior is unchanged.
vi.mock('$lib/api', async (importOriginal) => {
    const actual = await importOriginal<typeof import('$lib/api')>();
    return { ...actual, updateConfig: vi.fn() };
});

beforeEach(() => {
    // Polyfill matchMedia (useIsMobile) for jsdom — desktop: never matches.
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
    vi.mocked(updateConfig).mockReset();
});

afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
});

function makeConfig(): Config {
    return {
        workspace_dir: '/tmp/wt',
        database_path: '/tmp/db',
        version: '1.0.0',
        providers: [
            { id: 1, url: 'http://localhost:9999/v1', models: [{ name: 'model-a' }], concurrency: 1, max_requests_per_second: 0, stream_idle_seconds: 0 },
        ],
        worker_provider: '1',
        worker_model: 'model-a',
        utility_provider: '1',
        utility_model: 'model-a',
    };
}

describe('ConfigModal run-siblings-in-parallel save payload', () => {
    it('sends run_siblings_parallel: true when the toggle is on', async () => {
        vi.mocked(updateConfig).mockResolvedValue(makeConfig());
        const { container } = render(ConfigModal, {
            props: { config: makeConfig(), onClose: vi.fn(), onSaved: vi.fn() },
        });
        await waitFor(() => expect(container.querySelector('button.tab-btn')).toBeTruthy(), { timeout: 1000 });

        const tabs = Array.from(container.querySelectorAll('button.tab-btn'));
        fireEvent.click(tabs.find((b) => b.textContent?.trim() === TASK_TAB)!);
        await waitFor(() => expect(container.querySelector('button[aria-label="Run subtasks in parallel"]')).toBeTruthy(), { timeout: 1000 });

        // Flip the toggle on, and save.
        fireEvent.click(container.querySelector('button[aria-label="Run subtasks in parallel"]')!);
        fireEvent.click(container.querySelector('button[title="Save settings"]')!);
        await waitFor(() => expect(updateConfig).toHaveBeenCalledTimes(1), { timeout: 1000 });

        expect(vi.mocked(updateConfig).mock.calls[0][0]).toMatchObject({ run_siblings_parallel: true });
    });

    it('sends run_siblings_parallel: false when the toggle is left off', async () => {
        vi.mocked(updateConfig).mockResolvedValue(makeConfig());
        const { container } = render(ConfigModal, {
            props: { config: makeConfig(), onClose: vi.fn(), onSaved: vi.fn() },
        });
        await waitFor(() => expect(container.querySelector('button.tab-btn')).toBeTruthy(), { timeout: 1000 });

        const tabs = Array.from(container.querySelectorAll('button.tab-btn'));
        fireEvent.click(tabs.find((b) => b.textContent?.trim() === TASK_TAB)!);
        await waitFor(() => expect(container.querySelector('button[aria-label="Run subtasks in parallel"]')).toBeTruthy(), { timeout: 1000 });

        // Leave the toggle off and save.
        fireEvent.click(container.querySelector('button[title="Save settings"]')!);
        await waitFor(() => expect(updateConfig).toHaveBeenCalledTimes(1), { timeout: 1000 });

        expect(vi.mocked(updateConfig).mock.calls[0][0]).toMatchObject({ run_siblings_parallel: false });
    });
});

const RECLAIM = 'button[aria-label="Release a waiting task sandbox container"]';
const TASK_TAB = 'Task';

// The payload is what makes the row real: an omitted field keeps whatever was stored.
describe('ConfigModal release-idle-sandboxes save payload', () => {
    async function openDefaults(container: HTMLElement) {
        await waitFor(() => expect(container.querySelector('button.tab-btn')).toBeTruthy(), { timeout: 1000 });
        const tabs = Array.from(container.querySelectorAll('button.tab-btn'));
        fireEvent.click(tabs.find((b) => b.textContent?.trim() === TASK_TAB)!);
        await waitFor(() => expect(container.querySelector(RECLAIM)).toBeTruthy(), { timeout: 1000 });
    }

    it('sends reclaim_idle_sandbox: true when the toggle is on', async () => {
        vi.mocked(updateConfig).mockResolvedValue(makeConfig());
        const { container } = render(ConfigModal, {
            props: { config: makeConfig(), onClose: vi.fn(), onSaved: vi.fn() },
        });
        await openDefaults(container);

        fireEvent.click(container.querySelector(RECLAIM)!);
        fireEvent.click(container.querySelector('button[title="Save settings"]')!);
        await waitFor(() => expect(updateConfig).toHaveBeenCalledTimes(1), { timeout: 1000 });

        expect(vi.mocked(updateConfig).mock.calls[0][0]).toMatchObject({ reclaim_idle_sandbox: true });
    });

    it('sends reclaim_idle_sandbox: false when the toggle is left off', async () => {
        vi.mocked(updateConfig).mockResolvedValue(makeConfig());
        const { container } = render(ConfigModal, {
            props: { config: makeConfig(), onClose: vi.fn(), onSaved: vi.fn() },
        });
        await openDefaults(container);

        fireEvent.click(container.querySelector('button[title="Save settings"]')!);
        await waitFor(() => expect(updateConfig).toHaveBeenCalledTimes(1), { timeout: 1000 });

        expect(vi.mocked(updateConfig).mock.calls[0][0]).toMatchObject({ reclaim_idle_sandbox: false });
    });

    it('hydrates the toggle from a stored preference', async () => {
        const stored = { ...makeConfig(), reclaim_idle_sandbox: true };
        vi.mocked(updateConfig).mockResolvedValue(stored);
        const { container } = render(ConfigModal, {
            props: { config: stored, onClose: vi.fn(), onSaved: vi.fn() },
        });
        await openDefaults(container);

        expect(container.querySelector(RECLAIM)?.getAttribute('aria-checked')).toBe('true');
    });
});

const BREAKER = 'button[aria-label="Recover from failed turns automatically"]';

describe('ConfigModal loop-breaker save payload', () => {
    async function openDefaults(container: HTMLElement) {
        await waitFor(() => expect(container.querySelector('button.tab-btn')).toBeTruthy(), { timeout: 1000 });
        const tabs = Array.from(container.querySelectorAll('button.tab-btn'));
        fireEvent.click(tabs.find((b) => b.textContent?.trim() === TASK_TAB)!);
        await waitFor(() => expect(container.querySelector(BREAKER)).toBeTruthy(), { timeout: 1000 });
    }

    it('sends loop_breaker: true when the toggle is on', async () => {
        vi.mocked(updateConfig).mockResolvedValue(makeConfig());
        const { container } = render(ConfigModal, {
            props: { config: makeConfig(), onClose: vi.fn(), onSaved: vi.fn() },
        });
        await openDefaults(container);

        fireEvent.click(container.querySelector(BREAKER)!);
        fireEvent.click(container.querySelector('button[title="Save settings"]')!);
        await waitFor(() => expect(updateConfig).toHaveBeenCalledTimes(1), { timeout: 1000 });

        expect(vi.mocked(updateConfig).mock.calls[0][0]).toMatchObject({ loop_breaker: true });
    });

    it('sends loop_breaker: false when the toggle is left off', async () => {
        vi.mocked(updateConfig).mockResolvedValue(makeConfig());
        const { container } = render(ConfigModal, {
            props: { config: makeConfig(), onClose: vi.fn(), onSaved: vi.fn() },
        });
        await openDefaults(container);

        fireEvent.click(container.querySelector('button[title="Save settings"]')!);
        await waitFor(() => expect(updateConfig).toHaveBeenCalledTimes(1), { timeout: 1000 });

        expect(vi.mocked(updateConfig).mock.calls[0][0]).toMatchObject({ loop_breaker: false });
    });

    it('hydrates the toggle from a stored preference', async () => {
        const stored = { ...makeConfig(), loop_breaker: true };
        vi.mocked(updateConfig).mockResolvedValue(stored);
        const { container } = render(ConfigModal, {
            props: { config: stored, onClose: vi.fn(), onSaved: vi.fn() },
        });
        await openDefaults(container);

        expect(container.querySelector(BREAKER)?.getAttribute('aria-checked')).toBe('true');
    });
});
