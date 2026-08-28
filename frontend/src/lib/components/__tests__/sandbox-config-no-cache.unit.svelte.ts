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

const CHECKBOX_SELECTOR = 'input[aria-label="Disable build cache"]';
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

function checkbox(container: HTMLElement): HTMLInputElement {
    const el = container.querySelector<HTMLInputElement>(CHECKBOX_SELECTOR);
    if (!el) throw new Error('disable-cache checkbox not found');
    return el;
}

function rebuildButton(container: HTMLElement): HTMLButtonElement {
    const el = container.querySelector<HTMLButtonElement>('.btn-rebuild');
    if (!el) throw new Error('rebuild button not found');
    return el;
}

describe('SandboxConfigModal disable-cache checkbox', () => {
    beforeEach(() => {
        updateProject.mockReset().mockResolvedValue({});
        getProject.mockResolvedValue({ id: 'p1', active_terminal_ports: [] });
        getBoard.mockResolvedValue({ tasks: [] });
        buildSandboxImageStream.mockReset().mockResolvedValue({ status: 'built', image: 'img', project: 'p1' });
    });

    afterEach(() => {
        cleanup();
    });

    it('renders the checkbox next to the rebuild button, unchecked by default', async () => {
        const { container } = renderModal(projectWith());

        const box = checkbox(container);
        expect(box.checked).toBe(false);
        expect(box.disabled).toBe(false);

        // Inline: the checkbox and the rebuild button share the same action group
        const actions = box.closest('.build-actions');
        expect(actions).not.toBeNull();
        expect(actions!.contains(rebuildButton(container))).toBe(true);
    });

    it('omits no_cache (cache used) when rebuild is clicked untouched', async () => {
        const { container } = renderModal(projectWith());

        await fireEvent.click(rebuildButton(container));
        await waitFor(() => expect(buildSandboxImageStream).toHaveBeenCalled());

        expect(buildSandboxImageStream.mock.calls[0][2]).toEqual({ noCache: false });
    });

    it('passes noCache when the checkbox is ticked before rebuild', async () => {
        const { container } = renderModal(projectWith());

        // A plain click flips `checked` and fires the event bind:checked listens to.
        await fireEvent.click(checkbox(container));
        expect(checkbox(container).checked).toBe(true);

        await fireEvent.click(rebuildButton(container));
        await waitFor(() => expect(buildSandboxImageStream).toHaveBeenCalled());

        expect(buildSandboxImageStream.mock.calls[0][2]).toEqual({ noCache: true });
    });

    it('stays ticked for a follow-up rebuild (modal-scoped, not reset per click)', async () => {
        const { container } = renderModal(projectWith());

        await fireEvent.click(checkbox(container));
        await fireEvent.click(rebuildButton(container));
        await waitFor(() => expect(buildSandboxImageStream).toHaveBeenCalledTimes(1));
        await fireEvent.click(rebuildButton(container));
        await waitFor(() => expect(buildSandboxImageStream).toHaveBeenCalledTimes(2));

        expect(buildSandboxImageStream.mock.calls[1][2]).toEqual({ noCache: true });
    });

    it('is disabled while a build is running', async () => {
        let finish: (() => void) | undefined;
        buildSandboxImageStream.mockImplementation(() => new Promise((resolve) => {
            finish = () => resolve({ status: 'built', image: 'img', project: 'p1' });
        }));

        const { container } = renderModal(projectWith());
        await fireEvent.click(rebuildButton(container));
        await waitFor(() => expect(buildSandboxImageStream).toHaveBeenCalled());

        expect(checkbox(container).disabled).toBe(true);

        finish?.();
        await waitFor(() => expect(checkbox(container).disabled).toBe(false));
    });

    it('is not rendered when no Dockerfile is selected', async () => {
        const { container } = renderModal(projectWith({ sandbox_dockerfile_path: '' }));
        expect(container.querySelector(CHECKBOX_SELECTOR)).toBeNull();
    });
});
