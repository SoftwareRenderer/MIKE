import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';

/**
 * The whole write surface of the Sandbox Configuration screen: what one Save sends, in what
 * combination, and what refuses to send anything. These assertions are the payload contract the
 * screen had before it was split into per-section components, so they are written against the
 * values the screen produced, not the values its code looks like it should produce.
 */

// Mock raw asset imports and the icon component (not available in jsdom)
vi.mock('$lib/assets/icons/shield.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/shield-off.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/home.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/folder.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/file.svg?raw', () => ({ default: '' }));
vi.mock('$lib/components/Icon.svelte', () => ({ default: () => '' }));

// $lib/api is a barrel — keep every other export and override only the ones the modal calls.
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

// The disable-sandbox confirmation is answered by hand here; appStore stays the real one because
// the server-tool notice reads its config.
const { dialogStore } = vi.hoisted(() => ({ dialogStore: { showConfirm: vi.fn(), showToast: vi.fn() } }));
vi.mock('$lib/stores', async (importOriginal) => {
    const actual = await importOriginal<typeof import('$lib/stores')>();
    return { ...actual, dialogStore };
});

import SandboxConfigModal from '$lib/components/SandboxConfigModal.svelte';
import type { Project } from '$lib/types';

const MEMORY_INPUT = 'input[aria-label="Maximum memory a task’s sandbox may hold, in MB"]';
const ISOLATION_SWITCH = 'button[aria-label="Sandbox isolation"]';
const NETWORK_SWITCH = 'button[aria-label="Network isolation"]';

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

/** Nothing to write: the modal closes and no PUT leaves. */
async function saveWithoutWrite(container: HTMLElement, onClose: () => void) {
    await fireEvent.click(one<HTMLButtonElement>(container, '.btn-primary'));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(updateProject).not.toHaveBeenCalled();
}

async function expand(container: HTMLElement, ariaLabel: string) {
    const toggle = one<HTMLButtonElement>(container, `button[aria-label="${ariaLabel}"]`);
    if (toggle.getAttribute('aria-expanded') !== 'true') await fireEvent.click(toggle);
}

async function addEnv(container: HTMLElement, key: string, value: string) {
    await fireEvent.input(one<HTMLInputElement>(container, '.env-input-key'), { target: { value: key } });
    await fireEvent.input(one<HTMLInputElement>(container, '.env-input:not(.env-input-key)'), { target: { value } });
    await fireEvent.click(one<HTMLButtonElement>(container, '.btn-add-env'));
}

async function addMount(container: HTMLElement, host: string, containerPath: string, mode: 'rw' | 'ro' = 'rw') {
    await fireEvent.input(one<HTMLInputElement>(container, '.mount-input[aria-label="Host path"]'), { target: { value: host } });
    await fireEvent.input(one<HTMLInputElement>(container, '.mount-input[aria-label="Container path"]'), { target: { value: containerPath } });
    if (mode === 'ro') {
        await fireEvent.click(one<HTMLInputElement>(container, 'input[value="ro"]'));
    }
    await fireEvent.click(one<HTMLButtonElement>(container, '.btn-add-mount'));
}

async function addCapability(container: HTMLElement, cap: string) {
    const input = one<HTMLInputElement>(container, 'input[aria-label="Add capability"]');
    await fireEvent.input(input, { target: { value: cap } });
    await fireEvent.keyDown(input, { key: 'Enter' });
}

function imageSourceRadio(container: HTMLElement, label: 'Pre-built' | 'Dockerfile'): HTMLInputElement {
    const radio = Array.from(container.querySelectorAll<HTMLInputElement>('.radio-label input[type="radio"]'))
        .find((input) => (input.closest('.radio-label')?.textContent ?? '').includes(label));
    if (!radio) throw new Error(`image source radio not found: ${label}`);
    return radio;
}

describe('Sandbox Configuration save payload', () => {
    beforeAll(() => {
        // jsdom has no Web Animations API, and the slide transition calls it when a section opens.
        if (!Element.prototype.animate) {
            Element.prototype.animate = () =>
                ({ finished: Promise.resolve(), cancel() { }, play() { }, pause() { }, commit() { }, onfinish: null }) as unknown as Animation;
        }
    });

    beforeEach(() => {
        updateProject.mockReset().mockResolvedValue({});
        getProject.mockResolvedValue({ id: 'p1', active_terminal_ports: [] });
        getBoard.mockResolvedValue({ tasks: [] });
        buildSandboxImageStream.mockReset().mockResolvedValue({ status: 'built', image: 'img', project: 'p1' });
        dialogStore.showConfirm.mockReset().mockResolvedValue(true);
    });

    afterEach(() => cleanup());

    it('sends nothing at all when the form is untouched', async () => {
        const onClose = vi.fn();
        const { container } = renderModal(projectWith({
            sandbox_envs: [{ key: 'A', value: '1' }],
            sandbox_mounts: [{ host_path: '/host', container_path: '/container', mode: 'rw' }],
            sandbox_capabilities: ['CAP_NET_RAW'],
            forward_ports: [{ container_port: 8080 }],
        }), 0, onClose);
        await saveWithoutWrite(container, onClose);
    });

    it('writes every edited section in one PUT', async () => {
        const { container } = renderModal(projectWith({
            sandbox_envs: [{ key: 'A', value: '1' }],
            sandbox_mounts: [{ host_path: '/host', container_path: '/container', mode: 'rw' }],
            sandbox_capabilities: ['CAP_NET_RAW'],
            forward_ports: [{ container_port: 8080 }],
            sandbox_image: 'docker.io/library/ubuntu:24.04',
        }));

        await addEnv(container, 'B', 'two');
        await addMount(container, '/extra', '/mnt/extra', 'ro');
        await fireEvent.click(one<HTMLButtonElement>(container, '.btn-remove-mount'));
        await addCapability(container, 'CAP_SYS_TIME');
        await fireEvent.input(one<HTMLInputElement>(container, MEMORY_INPUT), { target: { value: '2048' } });
        await fireEvent.input(one<HTMLInputElement>(container, '.image-input'), { target: { value: 'docker.io/library/debian:12' } });

        expect(await save(container)).toStrictEqual({
            sandbox_envs: [{ key: 'A', value: '1' }, { key: 'B', value: 'two' }],
            sandbox_mounts: [{ host_path: '/extra', container_path: '/mnt/extra', mode: 'ro' }],
            sandbox_capabilities: ['CAP_NET_RAW', 'CAP_SYS_TIME'],
            sandbox_memory_limit: 2048,
            sandbox_image: 'docker.io/library/debian:12',
        });
    });

    it('leaves an empty env value as an empty string, never a bare name', async () => {
        const { container } = renderModal(projectWith({ sandbox_envs: [] }));
        await expand(container, 'Toggle environment variables settings');
        await addEnv(container, 'EMPTY_VAR', '');

        expect(await save(container)).toStrictEqual({ sandbox_envs: [{ key: 'EMPTY_VAR', value: '' }] });
    });

    it('writes an explicit empty list when the last mount is removed', async () => {
        const { container } = renderModal(projectWith({
            sandbox_mounts: [{ host_path: '/host', container_path: '/container', mode: 'rw' }],
        }));
        await fireEvent.click(one<HTMLButtonElement>(container, '.btn-remove-mount'));

        expect(await save(container)).toStrictEqual({ sandbox_mounts: [] });
    });

    it('writes a forward port under sandbox_forward_ports, interface included only when one is set', async () => {
        const { container } = renderModal(projectWith({ forward_ports: [] }));
        await expand(container, 'Toggle port forwarding settings');
        await fireEvent.input(one<HTMLInputElement>(container, '.port-input[aria-label="Container port"]'), { target: { value: '3000' } });
        await fireEvent.click(one<HTMLButtonElement>(container, '.btn-add-port'));

        expect(await save(container)).toStrictEqual({ sandbox_forward_ports: [{ container_port: 3000 }] });
    });

    it('switching from Dockerfile to a pre-built image clears the stored Dockerfile path', async () => {
        const { container } = renderModal(projectWith({
            sandbox_image: 'docker.io/library/ubuntu:24.04',
            sandbox_dockerfile_path: '/tmp/p1/Dockerfile',
        }));
        await fireEvent.click(imageSourceRadio(container, 'Pre-built'));

        expect(await save(container)).toStrictEqual({ sandbox_dockerfile_path: '' });
    });

    it('refuses a save with no Dockerfile while the Dockerfile source is selected', async () => {
        const { container } = renderModal(projectWith({ sandbox_image: 'docker.io/library/ubuntu:24.04' }));
        await fireEvent.click(imageSourceRadio(container, 'Dockerfile'));
        await fireEvent.input(one<HTMLInputElement>(container, MEMORY_INPUT), { target: { value: '4096' } });
        await fireEvent.click(one<HTMLButtonElement>(container, '.btn-primary'));
        await new Promise((resolve) => setTimeout(resolve, 0));

        expect(updateProject).not.toHaveBeenCalled();
        expect(one<HTMLElement>(container, '.error-message').textContent).toBe('Please select a Dockerfile before saving.');
    });

    it('clears the stored image whenever the Dockerfile source is the one in use', async () => {
        // A row can carry both, and the Dockerfile wins the source: the image is then dead
        // configuration and every save clears it.
        const { container } = renderModal(projectWith({
            sandbox_image: 'docker.io/library/ubuntu:24.04',
            sandbox_dockerfile_path: '/tmp/p1/Dockerfile',
        }));
        await fireEvent.input(one<HTMLInputElement>(container, MEMORY_INPUT), { target: { value: '4096' } });

        expect(await save(container)).toStrictEqual({ sandbox_image: '', sandbox_memory_limit: 4096 });
    });

    it('does not write network isolation when enabling sandbox turns it on by itself', async () => {
        const { container } = renderModal(projectWith({ sandbox_enabled: false, sandbox_network_isolation: false }));
        await fireEvent.click(one<HTMLButtonElement>(container, ISOLATION_SWITCH));
        expect(one<HTMLButtonElement>(container, NETWORK_SWITCH).getAttribute('aria-checked')).toBe('true');

        expect(await save(container)).toStrictEqual({ sandbox_enabled: true });
    });

    it('writes network isolation when the person moves that switch themselves', async () => {
        const { container } = renderModal(projectWith({ sandbox_enabled: true, sandbox_network_isolation: false }));
        await fireEvent.click(one<HTMLButtonElement>(container, NETWORK_SWITCH));

        expect(await save(container)).toStrictEqual({ sandbox_network_isolation: true });
    });

    it('blocks the save of a typed-but-un-added row and writes nothing', async () => {
        const { container } = renderModal(projectWith({ sandbox_envs: [] }));
        await expand(container, 'Toggle environment variables settings');
        await fireEvent.input(one<HTMLInputElement>(container, '.env-input-key'), { target: { value: 'DRAFT_ONLY' } });
        await fireEvent.click(one<HTMLButtonElement>(container, '.btn-primary'));
        await new Promise((resolve) => setTimeout(resolve, 0));

        expect(one<HTMLElement>(container, '.save-warning-message').textContent).toContain('un-added');
        expect(updateProject).not.toHaveBeenCalled();
    });

    it('refuses a duplicate variable name and a nameless one with the catalog sentences', async () => {
        const { container } = renderModal(projectWith({ sandbox_envs: [{ key: 'TAKEN', value: '1' }] }));
        await addEnv(container, '', 'no-name');
        expect(one<HTMLElement>(container, '.env-error').textContent).toBe('Environment variable name is required');

        await addEnv(container, 'TAKEN', 'again');
        expect(one<HTMLElement>(container, '.env-error').textContent).toBe('Duplicate variable name: TAKEN');
        expect(one<HTMLElement>(container, '.env-list').querySelectorAll('.env-item')).toHaveLength(1);
    });

    it('refuses a mount with a missing path the same way it always did', async () => {
        const { container } = renderModal(projectWith({ sandbox_mounts: [] }));
        await expand(container, 'Toggle mount settings');
        await fireEvent.click(one<HTMLButtonElement>(container, '.btn-add-mount'));
        expect(one<HTMLElement>(container, '.mount-error').textContent).toBe('Host path is required');

        await fireEvent.input(one<HTMLInputElement>(container, '.mount-input[aria-label="Host path"]'), { target: { value: '/host' } });
        await fireEvent.click(one<HTMLButtonElement>(container, '.btn-add-mount'));
        expect(one<HTMLElement>(container, '.mount-error').textContent).toBe('Container path is required');
        expect(updateProject).not.toHaveBeenCalled();
    });

    it('asks before disabling sandboxing and writes nothing when the answer is no', async () => {
        dialogStore.showConfirm.mockResolvedValue(false);
        const { container } = renderModal(projectWith({ sandbox_enabled: true }));
        await fireEvent.click(one<HTMLButtonElement>(container, ISOLATION_SWITCH));
        await fireEvent.click(one<HTMLButtonElement>(container, '.btn-primary'));
        await waitFor(() => expect(dialogStore.showConfirm).toHaveBeenCalled());
        await new Promise((resolve) => setTimeout(resolve, 0));

        expect(updateProject).not.toHaveBeenCalled();
        expect(one<HTMLButtonElement>(container, ISOLATION_SWITCH).getAttribute('aria-checked')).toBe('true');
    });

    it('writes sandbox_enabled false once the answer is yes', async () => {
        const { container } = renderModal(projectWith({ sandbox_enabled: true }));
        await fireEvent.click(one<HTMLButtonElement>(container, ISOLATION_SWITCH));

        expect(await save(container)).toStrictEqual({ sandbox_enabled: false });
        expect(dialogStore.showConfirm).toHaveBeenCalled();
    });

    it('shows no form at all while tasks are in progress', () => {
        const { container } = renderModal(projectWith(), 1);
        expect(container.querySelector('.locked-view')).not.toBeNull();
        expect(container.querySelector('.config-options')).toBeNull();
        expect(container.querySelector('.btn-primary')).toBeNull();
    });
});
