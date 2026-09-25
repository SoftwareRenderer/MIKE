import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, fireEvent, cleanup, waitFor } from '@testing-library/svelte';
import ConfigModal from '$lib/components/ConfigModal.svelte';
import type { Config, Project } from '$lib/types';
import { updateConfig, updateProject } from '$lib/api';
import { appStore } from '$lib/stores';

// Stub only the two writes; the rest of $lib/api keeps its behaviour.
vi.mock('$lib/api', async (importOriginal) => {
    const actual = await importOriginal<typeof import('$lib/api')>();
    return { ...actual, updateConfig: vi.fn(), updateProject: vi.fn() };
});

const TRAILER_TOGGLE = 'button[aria-label="Assisted-by line in commit messages"]';
const PROJECT_TAB = 'Project Defaults';

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
    vi.mocked(updateProject).mockReset();
});

afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    appStore.selectedProject = null;
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

function openProject(overrides: Partial<Project> = {}) {
    const project: Project = { id: 'proj-1', name: 'Atlas', path: '/tmp/atlas', created_at: '', updated_at: '', ...overrides };
    appStore.projects = [project];
    appStore.selectedProject = project;
    return project;
}

async function renderSettings(container: HTMLElement) {
    await waitFor(() => expect(container.querySelector('button.tab-btn')).toBeTruthy(), { timeout: 1000 });
    return Array.from(container.querySelectorAll('button.tab-btn')).map((b) => b.textContent?.trim());
}

async function openProjectTab(container: HTMLElement) {
    await renderSettings(container);
    const tabs = Array.from(container.querySelectorAll('button.tab-btn'));
    fireEvent.click(tabs.find((b) => b.textContent?.trim() === PROJECT_TAB)!);
    await waitFor(() => expect(container.querySelector(TRAILER_TOGGLE)).toBeTruthy(), { timeout: 1000 });
}

describe('Settings Project Defaults tab', () => {
    it('names no Project Defaults tab while no project is open', async () => {
        vi.mocked(updateConfig).mockResolvedValue(makeConfig());
        appStore.selectedProject = null;
        const { container } = render(ConfigModal, { props: { config: makeConfig(), onClose: vi.fn(), onSaved: vi.fn() } });

        expect(await renderSettings(container)).not.toContain(PROJECT_TAB);
    });

    it('reads a project row that never answered as on', async () => {
        vi.mocked(updateConfig).mockResolvedValue(makeConfig());
        openProject();
        const { container } = render(ConfigModal, { props: { config: makeConfig(), onClose: vi.fn(), onSaved: vi.fn() } });
        await openProjectTab(container);

        expect(container.querySelector(TRAILER_TOGGLE)?.getAttribute('aria-checked')).toBe('true');
    });

    it('writes the project row the answer was changed on', async () => {
        vi.mocked(updateConfig).mockResolvedValue(makeConfig());
        vi.mocked(updateProject).mockResolvedValue(openProject({ commit_assisted_by: false }));
        const { container } = render(ConfigModal, { props: { config: makeConfig(), onClose: vi.fn(), onSaved: vi.fn() } });
        await openProjectTab(container);

        expect(container.querySelector(TRAILER_TOGGLE)?.getAttribute('aria-checked')).toBe('false');

        fireEvent.click(container.querySelector(TRAILER_TOGGLE)!);
        fireEvent.click(container.querySelector('button[title="Save settings"]')!);
        await waitFor(() => expect(updateProject).toHaveBeenCalledTimes(1), { timeout: 1000 });

        expect(vi.mocked(updateProject).mock.calls[0]).toEqual(['proj-1', { commit_assisted_by: true }]);
        expect(appStore.selectedProject?.commit_assisted_by).toBe(true);
    });

    it('sends no project write when the row was never touched', async () => {
        vi.mocked(updateConfig).mockResolvedValue(makeConfig());
        vi.mocked(updateProject).mockResolvedValue(openProject({ commit_assisted_by: false }));
        const { container } = render(ConfigModal, { props: { config: makeConfig(), onClose: vi.fn(), onSaved: vi.fn() } });
        await openProjectTab(container);

        fireEvent.click(container.querySelector('button[title="Save settings"]')!);
        await waitFor(() => expect(updateConfig).toHaveBeenCalledTimes(1), { timeout: 1000 });

        expect(updateProject).not.toHaveBeenCalled();
    });
});
