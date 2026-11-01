import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';

/**
 * The build context field: the one place a person widens what an image build may `COPY`, so its
 * round trip through the draft, the payload and the clear-on-source-switch is the contract tested here.
 */

vi.mock('$lib/assets/icons/shield.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/shield-off.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/home.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/folder.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/file.svg?raw', () => ({ default: '' }));
vi.mock('$lib/components/Icon.svelte', () => ({ default: () => '' }));

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

const { dialogStore } = vi.hoisted(() => ({ dialogStore: { showConfirm: vi.fn(), showToast: vi.fn() } }));
vi.mock('$lib/stores', async (importOriginal) => {
    const actual = await importOriginal<typeof import('$lib/stores')>();
    return { ...actual, dialogStore };
});

import SandboxConfigModal from '$lib/components/SandboxConfigModal.svelte';
import type { Project } from '$lib/types';

const CONTEXT_FIELD = '#sandbox-build-context';
const DOCKERFILE_PATH = '/tmp/p1/Dockerfile';

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

function renderModal(project: Project) {
    return render(SandboxConfigModal, { props: { project, inProgressCount: 0, onClose: () => { }, onSaved: () => { } } });
}

function one<T extends Element>(container: HTMLElement, selector: string): T {
    const el = container.querySelector(selector);
    if (!el) throw new Error(`not found: ${selector}`);
    return el as T;
}

async function save(container: HTMLElement): Promise<Record<string, unknown>> {
    await fireEvent.click(one<HTMLButtonElement>(container, '.btn-primary'));
    await waitFor(() => expect(updateProject).toHaveBeenCalled());
    return updateProject.mock.calls[0][1] as Record<string, unknown>;
}

function imageSourceRadio(container: HTMLElement, label: 'Pre-built' | 'Dockerfile'): HTMLInputElement {
    const radio = Array.from(container.querySelectorAll<HTMLInputElement>('.radio-label input[type="radio"]'))
        .find((input) => (input.closest('.radio-label')?.textContent ?? '').includes(label));
    if (!radio) throw new Error(`image source radio not found: ${label}`);
    return radio;
}

describe('Sandbox Configuration build context', () => {
    beforeAll(() => {
        if (!Element.prototype.animate) {
            Element.prototype.animate = () => ({ finished: Promise.resolve(), cancel() { } }) as never;
        }
        updateProject.mockResolvedValue({});
        getProject.mockResolvedValue({});
        getBoard.mockResolvedValue({ projects: [] });
        buildSandboxImageStream.mockResolvedValue({ lines: [], done: true });
    });

    afterEach(() => {
        cleanup();
        vi.clearAllMocks();
    });

    it('sends only the context when that is the field that moved', async () => {
        const { container } = renderModal(projectWith());
        await fireEvent.input(one<HTMLInputElement>(container, CONTEXT_FIELD), { target: { value: 'services/api' } });

        expect(await save(container)).toStrictEqual({ sandbox_build_context: 'services/api' });
    });

    it('shows the stored subtree in the field', () => {
        const { container } = renderModal(projectWith({ sandbox_build_context: 'services/api' }));
        expect(one<HTMLInputElement>(container, CONTEXT_FIELD).value).toBe('services/api');
    });

    it('writes nothing when the field was left as the row has it', async () => {
        const { container } = renderModal(projectWith({ sandbox_build_context: 'services/api' }));
        await fireEvent.click(one<HTMLButtonElement>(container, '.btn-primary'));
        await waitFor(() => expect(updateProject).not.toHaveBeenCalled());
    });

    // The two image sources are alternatives, and the context belongs to one of them: a project built from
    // a registry image must not keep a subtree it will never stage.
    it('clears the context together with the Dockerfile when the source becomes a pre-built image', async () => {
        const { container } = renderModal(projectWith({
            sandbox_image: 'docker.io/library/ubuntu:24.04',
            sandbox_build_context: 'services/api',
        }));
        await fireEvent.click(imageSourceRadio(container, 'Pre-built'));

        expect(await save(container)).toStrictEqual({
            sandbox_dockerfile_path: '',
            sandbox_build_context: '',
        });
    });

    it('sends an explicit empty context when the person clears the field', async () => {
        const { container } = renderModal(projectWith({ sandbox_build_context: 'services/api' }));
        await fireEvent.input(one<HTMLInputElement>(container, CONTEXT_FIELD), { target: { value: '' } });

        expect(await save(container)).toStrictEqual({ sandbox_build_context: '' });
    });
});
