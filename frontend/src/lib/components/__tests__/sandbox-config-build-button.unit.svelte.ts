import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';

// Mock raw asset imports and the icon component (not available in jsdom)
vi.mock('$lib/assets/icons/shield.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/shield-off.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/home.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/folder.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/file.svg?raw', () => ({ default: '' }));
vi.mock('$lib/components/Icon.svelte', () => ({ default: () => '' }));

// $lib/api is a barrel — keep every other export and override only the ones the
// modal calls. The fns are hoisted so the mock factory can close over them.
const { updateProject, getProject, getBoard, buildSandboxImageStream } = vi.hoisted(() => ({
    updateProject: vi.fn(),
    getProject: vi.fn(),
    getBoard: vi.fn(),
    buildSandboxImageStream: vi.fn(),
}));
vi.mock('$lib/api', async (importOriginal) => {
    const actual = await importOriginal<typeof import('$lib/api')>();
    return { ...actual, updateProject, getProject, getBoard, buildSandboxImageStream };
});

import SandboxConfigModal from '$lib/components/SandboxConfigModal.svelte';
import type { Project } from '$lib/types';

const DOCKERFILE_PATH = '/tmp/repo/Dockerfile';

function projectWith(overrides: Partial<Project> = {}): Project {
    return {
        id: 'p1',
        name: 'Test Project',
        path: '/tmp/p1',
        sandbox_enabled: true,
        sandbox_network_isolation: false,
        sandbox_memory_limit: 1024,
        sandbox_dockerfile_path: DOCKERFILE_PATH,
        ...overrides,
    } as Project;
}

function renderModal(project: Project, inProgressCount = 0) {
    return render(SandboxConfigModal, {
        props: { project, inProgressCount, onClose: () => { }, onSaved: () => { } },
    });
}

function rebuildButton(container: HTMLElement): HTMLButtonElement {
    const el = container.querySelector<HTMLButtonElement>('.btn-rebuild');
    if (!el) throw new Error('rebuild button not found');
    return el;
}

describe('SandboxConfigModal rebuild button', () => {
    beforeEach(() => {
        updateProject.mockReset().mockResolvedValue({});
        getProject.mockResolvedValue({ id: 'p1', active_terminal_ports: [] });
        getBoard.mockResolvedValue({ tasks: [] });
        buildSandboxImageStream.mockReset().mockResolvedValue({ status: 'built', image: 'localhost/mike-p1:1f2e3d' });
    });

    afterEach(() => {
        cleanup();
    });

    it('starts the build the row names, with no modifier to choose', async () => {
        const { container } = renderModal(projectWith());

        await fireEvent.click(rebuildButton(container));
        await waitFor(() => expect(buildSandboxImageStream).toHaveBeenCalled());

        expect(buildSandboxImageStream).toHaveBeenCalledWith('p1', expect.any(Function));
    });

    it('is disabled while a build runs and live again once it ends', async () => {
        let finish: (() => void) | undefined;
        buildSandboxImageStream.mockImplementation(() => new Promise((resolve) => {
            finish = () => resolve({ status: 'built', image: 'localhost/mike-p1:1f2e3d' });
        }));

        const { container } = renderModal(projectWith());
        await fireEvent.click(rebuildButton(container));
        await waitFor(() => expect(buildSandboxImageStream).toHaveBeenCalled());
        expect(rebuildButton(container).disabled).toBe(true);

        finish?.();
        await waitFor(() => expect(rebuildButton(container).disabled).toBe(false));
    });

    it('offers no build control when the row names no Dockerfile', async () => {
        const { container } = renderModal(projectWith({ sandbox_dockerfile_path: '' }));
        expect(container.querySelector('.btn-rebuild')).toBeNull();
        expect(container.querySelector('.btn-build')).toBeNull();
    });
});
