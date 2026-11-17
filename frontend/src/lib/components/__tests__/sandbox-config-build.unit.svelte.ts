import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';

/**
 * THE IMAGE BUILD of the Sandbox Configuration screen.
 *
 * One project image is prepared at a time, and the screen that opens mid-build follows the build it did
 * not start rather than starting a second one. Three things hold that: the button stages the row before
 * the container starts, the build context is the one subtree the build may `COPY` and belongs to the
 * Dockerfile source alone, and exactly one reader watches the stream — the dialog that started a build is
 * already reading it, so a second reader would print every line twice.
 *
 * What the draft refuses and what a Save carries live in `sandbox-config-draft.unit.svelte.ts` and
 * `sandbox-config-payload.unit.svelte.ts`.
 */

// Mock raw asset imports and the icon component (not available in jsdom)
vi.mock('$lib/assets/icons/shield.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/shield-off.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/home.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/folder.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/file.svg?raw', () => ({ default: '' }));
vi.mock('$lib/components/Icon.svelte', () => ({ default: () => '' }));

// $lib/api is a barrel — keep every other export and override only the ones the modal calls. The fns are
// hoisted so the mock factory can close over them.
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

// The disable-sandbox confirmation is answered by hand here; appStore stays the real one because the
// server-tool notice reads its config.
const { dialogStore } = vi.hoisted(() => ({ dialogStore: { showConfirm: vi.fn(), showToast: vi.fn() } }));
vi.mock('$lib/stores', async (importOriginal) => {
    const actual = await importOriginal<typeof import('$lib/stores')>();
    return { ...actual, dialogStore };
});

import SandboxConfigModal from '$lib/components/SandboxConfigModal.svelte';
import { SandboxDraftOwner } from '$lib/components/SandboxConfigModal/draft.svelte';
import type { Project } from '$lib/types';

const CONTEXT_FIELD = '#sandbox-build-context';
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

function one<T extends Element>(container: HTMLElement, selector: string): T {
    const el = container.querySelector(selector);
    if (!el) throw new Error(`not found: ${selector}`);
    return el as T;
}

function rebuildButton(container: HTMLElement): HTMLButtonElement {
    return one<HTMLButtonElement>(container, '.btn-rebuild');
}

/** Save and hand back what the one PUT carried. */
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

function buildLines(container: HTMLElement): string[] {
    return [...container.querySelectorAll('.build-log-line')].map((el) => el.textContent ?? '');
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
    buildSandboxImageStream.mockReset().mockResolvedValue({ status: 'built', image: 'localhost/mike-p1:1f2e3d' });
    watchSandboxImageBuild.mockReset().mockResolvedValue(false);
    cancelSandboxImageBuild.mockReset().mockResolvedValue(true);
    dialogStore.showConfirm.mockReset().mockResolvedValue(true);
});

afterEach(() => {
    cleanup();
});

describe('the rebuild button', () => {
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

    // The build stages the row, not the screen: a field typed and then built from without pressing Save
    // still has to reach the server before the container starts.
    it('stores an unsaved build context before the build that stages it', async () => {
        const order: string[] = [];
        updateProject.mockImplementation(async () => { order.push('write'); return {}; });
        buildSandboxImageStream.mockImplementation(async () => { order.push('build'); return { status: 'built' }; });

        const { container } = renderModal(projectWith());
        await fireEvent.input(one<HTMLInputElement>(container, CONTEXT_FIELD), { target: { value: 'services/api' } });
        await fireEvent.click(rebuildButton(container));

        await waitFor(() => expect(buildSandboxImageStream).toHaveBeenCalled());
        expect(order).toStrictEqual(['write', 'build']);
        expect(updateProject).toHaveBeenCalledWith('p1', { sandbox_build_context: 'services/api' });
    });
});

describe('the build context', () => {
    it('sends only the context when that is the field that moved', async () => {
        const { container } = renderModal(projectWith());
        await fireEvent.input(one<HTMLInputElement>(container, CONTEXT_FIELD), { target: { value: 'services/api' } });

        expect(await save(container)).toStrictEqual({ sandbox_build_context: 'services/api' });
    });

    it('shows the stored subtree in the field', () => {
        const { container } = renderModal(projectWith({ sandbox_build_context: 'services/api' }));
        expect(one<HTMLInputElement>(container, CONTEXT_FIELD).value).toBe('services/api');
    });

    // An empty field is not the repository root, and a person who believes it is gets a COPY that finds
    // nothing. The field names the subtree an empty value stages.
    it('names the Dockerfile\'s own folder as the subtree an empty field stages', () => {
        const { container } = renderModal(projectWith({
            path: '/srv/repo',
            sandbox_dockerfile_path: '/srv/repo/examples/kanban.Dockerfile',
        }));
        expect(one<HTMLInputElement>(container, CONTEXT_FIELD).placeholder).toBe('examples');
    });

    it('writes nothing when the field was left as the row has it', async () => {
        const { container } = renderModal(projectWith({ sandbox_build_context: 'services/api' }));
        await fireEvent.click(one<HTMLButtonElement>(container, '.btn-primary'));
        await waitFor(() => expect(updateProject).not.toHaveBeenCalled());
    });

    it('sends an explicit empty context when the person clears the field', async () => {
        const { container } = renderModal(projectWith({ sandbox_build_context: 'services/api' }));
        await fireEvent.input(one<HTMLInputElement>(container, CONTEXT_FIELD), { target: { value: '' } });

        expect(await save(container)).toStrictEqual({ sandbox_build_context: '' });
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
});

describe('the build already running', () => {
    it('shows the phase and the lines of a build it did not start', async () => {
        const build = runningBuild();
        const { container } = renderModal(projectWith());
        await waitFor(() => expect(watchSandboxImageBuild).toHaveBeenCalledWith('p1', expect.any(Function), expect.any(AbortSignal)));

        build.emit({ phase: 'building_image', image: 'localhost/sandbox-test-project' });
        build.emit({ phase: 'building_image', line: 'step 1/3 FROM docker.io/library/node:22' });

        await waitFor(() => expect(buildLines(container)).toContain('step 1/3 FROM docker.io/library/node:22'));
        expect(rebuildButton(container).disabled).toBe(true);
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
        expect(rebuildButton(container).disabled).toBe(false);
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

// The screen reads one build at a time.
describe('SandboxDraftOwner reads one build at a time', () => {
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
