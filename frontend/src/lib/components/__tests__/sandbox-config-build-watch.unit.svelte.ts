import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';

// Mock raw asset imports and the icon component (not available in jsdom)
vi.mock('$lib/assets/icons/shield.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/shield-off.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/home.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/folder.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/file.svg?raw', () => ({ default: '' }));
vi.mock('$lib/components/Icon.svelte', () => ({ default: () => '' }));

// $lib/api is a barrel — keep every other export and override only the ones the modal calls.
const { updateProject, getProject, getBoard, buildSandboxImageStream, watchSandboxImageBuild, cancelSandboxImageBuild } = vi.hoisted(() => ({
    updateProject: vi.fn(),
    getProject: vi.fn(),
    getBoard: vi.fn(),
    buildSandboxImageStream: vi.fn(),
    watchSandboxImageBuild: vi.fn(),
    cancelSandboxImageBuild: vi.fn(),
}));
vi.mock('$lib/api', async (importOriginal) => {
    const actual = await importOriginal<typeof import('$lib/api')>();
    return { ...actual, updateProject, getProject, getBoard, buildSandboxImageStream, watchSandboxImageBuild, cancelSandboxImageBuild };
});

import SandboxConfigModal from '$lib/components/SandboxConfigModal.svelte';
import { SandboxDraftOwner } from '$lib/components/SandboxConfigModal/draft.svelte';
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

/**
 * A build this project was already running when the screen opened. The test pushes its frames and leaves the
 * stream open, because the build has not finished.
 */
function runningBuild() {
    let report: ((status: Record<string, string>) => void) | undefined;
    let signal: AbortSignal | undefined;
    watchSandboxImageBuild.mockImplementation(async (_id, onStatus, watching: AbortSignal) => {
        report = onStatus;
        signal = watching;
        return await new Promise<boolean>(() => { });
    });
    return {
        emit: (status: Record<string, string>) => report?.(status),
        // The screen closed, so the watching ended. The build itself keeps running.
        aborted: () => signal?.aborted === true,
    };
}

function buildLines(container: HTMLElement): string[] {
    return [...container.querySelectorAll('.build-log-line')].map((el) => el.textContent ?? '');
}

describe('SandboxConfigModal follows the build already running', () => {
    beforeEach(() => {
        updateProject.mockReset().mockResolvedValue({});
        getProject.mockResolvedValue({ id: 'p1', active_terminal_ports: [] });
        getBoard.mockResolvedValue({ tasks: [] });
        buildSandboxImageStream.mockReset().mockResolvedValue({ status: 'built' });
        watchSandboxImageBuild.mockReset().mockResolvedValue(false);
        cancelSandboxImageBuild.mockReset().mockResolvedValue(true);
    });

    afterEach(() => {
        cleanup();
    });

    it('shows the phase and the lines of a build it did not start', async () => {
        const build = runningBuild();
        const { container } = renderModal(projectWith());
        await waitFor(() => expect(watchSandboxImageBuild).toHaveBeenCalledWith('p1', expect.any(Function), expect.any(AbortSignal)));

        build.emit({ phase: 'building_image', image: 'localhost/sandbox-test-project' });
        build.emit({ phase: 'building_image', line: 'step 1/3 FROM docker.io/library/node:22' });

        await waitFor(() => expect(buildLines(container)).toContain('step 1/3 FROM docker.io/library/node:22'));
        const rebuild = container.querySelector<HTMLButtonElement>('.btn-rebuild');
        expect(rebuild?.disabled).toBe(true);
    });

    it('offers to stop a build it only watches, and says so when it stopped', async () => {
        const build = runningBuild();
        const { container } = renderModal(projectWith());
        await waitFor(() => expect(watchSandboxImageBuild).toHaveBeenCalled());
        build.emit({ phase: 'building_image', image: 'localhost/sandbox-test-project' });

        const cancel = await waitFor(() => {
            const el = container.querySelector<HTMLButtonElement>('.btn-cancel-build');
            expect(el).not.toBeNull();
            return el!;
        });
        await fireEvent.click(cancel);
        await waitFor(() => expect(cancelSandboxImageBuild).toHaveBeenCalledWith('p1'));

        build.emit({ phase: 'cancelled', image: 'localhost/sandbox-test-project' });
        await waitFor(() => expect(container.querySelector('.build-cancelled')).not.toBeNull());
        // A build the person stopped is not a fault: the screen says what happened and lets them build again.
        expect(container.querySelector('.build-error')).toBeNull();
        expect(container.querySelector<HTMLButtonElement>('.btn-rebuild')?.disabled).toBe(false);
        expect(container.querySelector('.btn-cancel-build')).toBeNull();
    });

    it('draws no log and no cancel button for an idle project', async () => {
        const { container } = renderModal(projectWith());
        await waitFor(() => expect(watchSandboxImageBuild).toHaveBeenCalled());
        expect(container.querySelector('.build-log')).toBeNull();
        expect(container.querySelector('.btn-cancel-build')).toBeNull();
    });

    // A task waiting for its image is a task in progress, and a task in progress is what puts the locked view
    // on the screen. That is where this build is most often read.
    it('shows the running build while the form is locked', async () => {
        const build = runningBuild();
        const { container } = renderModal(projectWith(), 1);
        await waitFor(() => expect(watchSandboxImageBuild).toHaveBeenCalled());
        expect(container.querySelector('.locked-view')).not.toBeNull();

        build.emit({ phase: 'building_image', image: 'localhost/sandbox-test-project' });
        build.emit({ phase: 'building_image', line: 'building in a gVisor container' });

        await waitFor(() => expect(buildLines(container)).toContain('building in a gVisor container'));
        expect(container.querySelector('.btn-cancel-build')).not.toBeNull();
    });

    it('stops watching when the screen closes, and leaves the build running', async () => {
        const build = runningBuild();
        const { unmount } = renderModal(projectWith());
        await waitFor(() => expect(watchSandboxImageBuild).toHaveBeenCalled());
        expect(build.aborted()).toBe(false);

        unmount();
        await waitFor(() => expect(build.aborted()).toBe(true));
    });

    it('follows no build for a project that runs no sandbox', async () => {
        const { container } = renderModal(projectWith({ sandbox_enabled: false }));
        // The form drew, so the screen has decided, and it did not ask about a build.
        await waitFor(() => expect(container.querySelector('.sandbox-config')).not.toBeNull());
        expect(watchSandboxImageBuild).not.toHaveBeenCalled();
    });
});

// The screen reads one build at a time. The dialog that started a build is already reading its stream, and a
// second reader would print every line twice.
describe('SandboxDraftOwner reads one build at a time', () => {
    beforeEach(() => {
        watchSandboxImageBuild.mockReset().mockResolvedValue(false);
        buildSandboxImageStream.mockReset();
        updateProject.mockReset().mockResolvedValue({});
    });

    it('opens one watch for one screen', () => {
        watchSandboxImageBuild.mockImplementation(() => new Promise(() => { }));
        const draft = new SandboxDraftOwner();
        draft.watchBuild('p1');
        draft.watchBuild('p1');
        expect(watchSandboxImageBuild).toHaveBeenCalledTimes(1);
    });

    it('watches nothing while it is reading the build it started', async () => {
        let finish: (() => void) | undefined;
        buildSandboxImageStream.mockImplementation(() => new Promise((resolve) => {
            finish = () => resolve({ status: 'built' });
        }));
        const draft = new SandboxDraftOwner();
        draft.dockerfilePath = 'Dockerfile';
        const started = draft.rebuild({ id: 'p1', name: 'p', sandbox_dockerfile_path: 'Dockerfile' } as Project);

        await waitFor(() => expect(buildSandboxImageStream).toHaveBeenCalled());
        await draft.watchBuild('p1');
        expect(watchSandboxImageBuild).not.toHaveBeenCalled();

        finish?.();
        await started;
    });
});
