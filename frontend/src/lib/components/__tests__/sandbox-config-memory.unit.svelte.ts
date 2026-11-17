import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';

// Mock raw asset imports and the icon component (not available in jsdom)
vi.mock('$lib/assets/icons/shield.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/shield-off.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/home.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/folder.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/file.svg?raw', () => ({ default: '' }));
vi.mock('$lib/components/Icon.svelte', () => ({ default: () => '' }));

// $lib/api is a barrel — keep every other export and override only the four
// the modal calls. The fns are hoisted so the mock factory can close over them.
const { updateProject, getProject, getBoard } = vi.hoisted(() => ({
    updateProject: vi.fn(),
    getProject: vi.fn(),
    getBoard: vi.fn(),
}));
vi.mock('$lib/api', async (importOriginal) => {
    const actual = await importOriginal<typeof import('$lib/api')>();
    return { ...actual, updateProject, getProject, getBoard, buildSandboxImageStream: vi.fn() };
});

import SandboxConfigModal from '$lib/components/SandboxConfigModal.svelte';
import type { Project } from '$lib/types';

const INPUT_SELECTOR = 'input[aria-label="Maximum memory a task’s sandbox may hold, in MB"]';

function projectWith(overrides: Partial<Project> = {}): Project {
    return {
        id: 'p1',
        name: 'Test Project',
        path: '/tmp/p1',
        sandbox_enabled: true,
        sandbox_network_isolation: false,
        sandbox_memory_limit: 1024,
        ...overrides,
    } as Project;
}

function renderModal(project: Project, inProgressCount = 0, onClose = () => { }) {
    return render(SandboxConfigModal, {
        props: { project, inProgressCount, onClose, onSaved: () => { } },
    });
}

function memoryInput(container: HTMLElement): HTMLInputElement {
    const el = container.querySelector<HTMLInputElement>(INPUT_SELECTOR);
    if (!el) throw new Error('memory limit input not found');
    return el;
}

/** Type into the memory field (Svelte binding) and hit Save. */
async function saveWith(container: HTMLElement, value: string) {
    await fireEvent.input(memoryInput(container), { target: { value } });
    const save = container.querySelector<HTMLButtonElement>('.btn-primary');
    if (!save) throw new Error('save button not found');
    await fireEvent.click(save);
    await waitFor(() => expect(updateProject).toHaveBeenCalled());
    return updateProject.mock.calls[0][1] as Record<string, unknown>;
}

describe('SandboxConfigModal memory limit', () => {
    beforeEach(() => {
        updateProject.mockReset().mockResolvedValue({});
        getProject.mockResolvedValue({ id: 'p1', active_terminal_ports: [] });
        getBoard.mockResolvedValue({ tasks: [] });
    });

    afterEach(() => {
        cleanup();
    });

    // A blank field is not an absent field: a null would be ignored by the server and leave the old
    // limit in place, so the screen writes the 0 that means "no limit".
    it.each([
        { stored: 1024, typed: '', saved: 0, name: 'a blanked field' },
        { stored: 1024, typed: '-512', saved: 0, name: 'a negative value' },
        { stored: 1024, typed: '2048', saved: 2048, name: 'an explicit limit' },
    ])('saves $name as the limit the server is told', async ({ stored, typed, saved }) => {
        const { container } = renderModal(projectWith({ sandbox_memory_limit: stored }));
        expect(memoryInput(container).value).toBe(String(stored));

        expect(await saveWith(container, typed)).toEqual({ sandbox_memory_limit: saved });
    });

    it('renders an unlimited project as a blank field with a "No limit" placeholder', () => {
        const { container } = renderModal(projectWith({ sandbox_memory_limit: 0 }));
        const input = memoryInput(container);

        expect(input.value).toBe('');
        expect(input.placeholder).toBe('No limit');
    });

    it('sends nothing when a blank field is saved unchanged', async () => {
        const closed = vi.fn();
        const { container } = renderModal(projectWith({ sandbox_memory_limit: 0 }), 0, closed);
        await fireEvent.input(memoryInput(container), { target: { value: '' } });
        await fireEvent.click(container.querySelector<HTMLButtonElement>('.btn-primary')!);

        // No diff → the modal closes without a PUT (the limit stays unlimited).
        await waitFor(() => expect(closed).toHaveBeenCalled());
        expect(updateProject).not.toHaveBeenCalled();
    });

    it.each([
        { limit: 0, shown: 'No limit' },
        { limit: 2048, shown: '2048 MB' },
    ])('reports $shown in the read-only locked view', ({ limit, shown }) => {
        const { container } = renderModal(projectWith({ sandbox_memory_limit: limit }), 1);
        const rows = Array.from(container.querySelectorAll('.sandbox-row'));
        const memoryRow = rows.find((r) => r.querySelector('.sandbox-label')?.textContent?.includes('Memory Limit'));

        expect(container.querySelector('.locked-view')).not.toBeNull();
        expect(memoryRow?.querySelector('.sandbox-value')?.textContent?.trim()).toBe(shown);
    });
});
