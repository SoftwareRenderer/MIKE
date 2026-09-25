import { describe, it, expect, vi, beforeEach } from 'vitest';

// The owner reaches the API for the active-port read and the image build only.
const { getProject, getBoard, updateProject, buildSandboxImageStream } = vi.hoisted(() => ({
    getProject: vi.fn(),
    getBoard: vi.fn(),
    updateProject: vi.fn(),
    buildSandboxImageStream: vi.fn(),
}));
vi.mock('$lib/api', async (importOriginal) => {
    const actual = await importOriginal<typeof import('$lib/api')>();
    return { ...actual, getProject, getBoard, updateProject, buildSandboxImageStream };
});

import { SandboxDraftOwner } from '$lib/components/SandboxConfigModal/draft.svelte';
import { projectChanges } from '$lib/components/SandboxConfigModal/drafts';
import type { Project } from '$lib/types';

/**
 * The draft the Sandbox Configuration edits. Every section writes this one holder, so the rules that
 * decide what a Save will carry live here rather than in any section: the auto-answer isolation gets
 * when sandboxing is switched on, the refusal of a row that cannot be added, and the `KEY=VALUE`
 * shape an environment declaration must keep.
 */

function storedRow(overrides: Partial<Project> = {}): Project {
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

function draftOf(row: Project): SandboxDraftOwner {
    const draft = new SandboxDraftOwner();
    draft.reseed(row);
    return draft;
}

function keydown(key: string, init: KeyboardEventInit = {}): KeyboardEvent {
    return new window.KeyboardEvent('keydown', { key, cancelable: true, ...init });
}

describe('the sandbox draft', () => {
    beforeEach(() => {
        getProject.mockReset().mockResolvedValue({ id: 'p1', active_terminal_ports: [] });
        getBoard.mockReset().mockResolvedValue({ tasks: [] });
        updateProject.mockReset().mockResolvedValue({});
        buildSandboxImageStream.mockReset().mockResolvedValue({});
    });

    it('starts as the stored row and offers nothing to write', () => {
        const row = storedRow({
            sandbox_envs: [{ key: 'A', value: '1' }],
            sandbox_mounts: [{ host_path: '/host', container_path: '/mnt', mode: 'ro' }],
            sandbox_capabilities: ['CAP_NET_RAW'],
            forward_ports: [{ container_port: 8080 }],
        });
        const draft = draftOf(row);

        expect(draft.memoryLimitMB).toBe(1024);
        expect(draft.envsExpanded).toBe(true);
        expect(draft.hasActivePorts).toBe(false);
        expect(projectChanges(row, draft.snapshot()).changes).toEqual({});
    });

    it('shows a blank memory field for a project with no limit', () => {
        const draft = draftOf(storedRow({ sandbox_memory_limit: 0 }));
        expect(draft.memoryLimitMB).toBeNull();
    });

    describe('the isolation switch', () => {
        it('answers isolation itself when sandboxing is switched on', () => {
            const row = storedRow({ sandbox_enabled: false, sandbox_network_isolation: false });
            const draft = draftOf(row);

            draft.sandboxEnabled = true;
            draft.onSandboxEnabledChanged();

            expect(draft.networkIsolation).toBe(true);
            // The auto-answer is not the person's own, so a Save does not write it.
            expect(projectChanges(row, draft.snapshot()).changes).toEqual({ sandbox_enabled: true });
        });

        it('leaves isolation alone once the person has moved that switch', () => {
            const row = storedRow({ sandbox_enabled: false, sandbox_network_isolation: false });
            const draft = draftOf(row);

            draft.markNetworkTouched();
            draft.networkIsolation = true;
            draft.onSandboxEnabledChanged();
            expect(projectChanges(row, draft.snapshot()).changes).toEqual({ sandbox_network_isolation: true });

            draft.networkIsolation = false;
            draft.onSandboxEnabledChanged();
            expect(draft.networkIsolation).toBe(false);
        });

        it('keeps the row\'s answer when the person declines to disable sandboxing', () => {
            const row = storedRow({ sandbox_enabled: true });
            const draft = draftOf(row);

            draft.sandboxEnabled = false;
            draft.onSandboxEnabledChanged();
            expect(projectChanges(row, draft.snapshot()).changes).toEqual({ sandbox_enabled: false });

            draft.restoreSandboxEnabled(row);
            expect(draft.sandboxEnabled).toBe(true);
            expect(projectChanges(row, draft.snapshot()).changes).toEqual({});
        });
    });

    describe('environment variables', () => {
        it('adds a declaration as a key with a value, even when the value is empty', () => {
            const draft = draftOf(storedRow({ sandbox_envs: [] }));
            draft.newEnvKey = 'EMPTY';
            draft.addEnv();

            expect(draft.envs).toEqual([{ key: 'EMPTY', value: '' }]);
        });

        it('refuses a nameless and a duplicated declaration with the catalog sentences', () => {
            const draft = draftOf(storedRow({ sandbox_envs: [{ key: 'TAKEN', value: '1' }] }));

            draft.newEnvKey = ' ';
            draft.addEnv();
            expect(draft.envError).toBe('Environment variable name is required');
            expect(draft.envs).toHaveLength(1);

            draft.newEnvKey = 'TAKEN';
            draft.addEnv();
            expect(draft.envError).toBe('Duplicate variable name: TAKEN');
            expect(draft.envs).toHaveLength(1);
        });

        it('replaces the declaration being edited and nothing else', () => {
            const draft = draftOf(storedRow({ sandbox_envs: [{ key: 'A', value: '1' }, { key: 'B', value: '2' }] }));

            draft.editEnv(0);
            draft.newEnvKey = 'A2';
            draft.newEnvValue = '9';
            draft.addEnv();

            expect(draft.envs).toEqual([{ key: 'A2', value: '9' }, { key: 'B', value: '2' }]);
            expect(draft.editingEnvIndex).toBeNull();
        });
    });

    describe('bind mounts', () => {
        it('refuses a path that is missing, and never writes one', () => {
            const row = storedRow({ sandbox_mounts: [] });
            const draft = draftOf(row);

            draft.addMount();
            expect(draft.mountError).toBe('Host path is required');
            draft.newMountHostPath = '/host';
            draft.addMount();
            expect(draft.mountError).toBe('Container path is required');
            expect(draft.mounts).toEqual([]);
        });

        it('trims what was typed and keeps the chosen mode', () => {
            const draft = draftOf(storedRow({ sandbox_mounts: [] }));
            draft.newMountHostPath = ' /host ';
            draft.newMountContainerPath = ' /mnt ';
            draft.newMountMode = 'ro';
            draft.addMount();

            expect(draft.mounts).toEqual([{ host_path: '/host', container_path: '/mnt', mode: 'ro' }]);
            // The next row starts empty and read-write again.
            expect(draft.newMountMode).toBe('rw');
            expect(draft.hasPendingMount).toBe(false);
        });
    });

    describe('forwarded ports', () => {
        it('leaves the interface out when every interface is meant', () => {
            const draft = draftOf(storedRow({ forward_ports: [] }));
            draft.newContainerPort = '3000';
            draft.newHostIP = '0.0.0.0';
            draft.addPortForward();

            expect(draft.forwardPorts).toEqual([{ container_port: 3000 }]);
        });

        it('refuses a port out of range or an interface that is not an address', () => {
            const draft = draftOf(storedRow({ forward_ports: [] }));

            draft.newContainerPort = '70000';
            draft.addPortForward();
            expect(draft.portError).toBe('Container port must be between 1 and 65535');

            draft.newContainerPort = '80';
            draft.newHostIP = 'saturn.lan';
            draft.addPortForward();
            expect(draft.portError).toContain('IP address');
            expect(draft.forwardPorts).toEqual([]);
        });

        it('opens an existing row on the interface it is published on', () => {
            const draft = draftOf(storedRow({ forward_ports: [{ container_port: 80 }] }));
            draft.editPort(0);

            expect(draft.newHostIP).toBe('0.0.0.0');
            draft.newHostIP = '192.168.1.100';
            draft.addPortForward();
            expect(draft.forwardPorts).toEqual([{ container_port: 80, host_ip: '192.168.1.100' }]);
        });

        it('names a typed-but-un-added row so a Save can refuse it', () => {
            const draft = draftOf(storedRow());
            expect(draft.hasPendingRow).toBe(false);

            draft.newEnvKey = 'NOT_ADDED';
            expect(draft.hasPendingRow).toBe(true);
        });

        it('merges the project terminal with what running tasks publish', async () => {
            getProject.mockResolvedValue({ id: 'p1', active_terminal_ports: [{ container_port: 9000, host_port: 31000 }] });
            getBoard.mockResolvedValue({
                tasks: [
                    { id: 't1', execution_state: 'running', assigned_ports: [{ container_port: 3000, host_port: 32000 }], status: 'in_progress' },
                    { id: 't2', execution_state: 'paused', assigned_ports: [{ container_port: 4000, host_port: 33000 }], status: 'in_progress' },
                ],
            });
            const draft = draftOf(storedRow());

            const tasks = await draft.loadActivePorts('p1');

            expect([...draft.activeHostPorts.entries()]).toEqual([[9000, 31000], [3000, 32000]]);
            expect(draft.hasActivePorts).toBe(true);
            expect(tasks?.filter(t => t.status === 'in_progress')).toHaveLength(2);
        });

        it('says nothing about what is running when the board cannot be read', async () => {
            getBoard.mockRejectedValue(new Error('offline'));
            const draft = draftOf(storedRow());

            expect(await draft.loadActivePorts('p1')).toBeNull();
        });
    });

    describe('capabilities', () => {
        it('adds a name whatever case it was typed in, once', () => {
            const draft = draftOf(storedRow({ sandbox_capabilities: [] }));
            draft.addCapability(' cap_net_raw ');
            draft.addCapability('CAP_NET_RAW');

            expect(draft.capabilities).toEqual(['CAP_NET_RAW']);
        });

        it('walks the suggestions into the field and adds what is selected', () => {
            const draft = draftOf(storedRow({ sandbox_capabilities: [] }));
            draft.capInputValue = 'CAP_NET';
            const offered = draft.capSuggestions;
            expect(offered.length).toBeGreaterThan(1);

            draft.handleCapInputKeydown(keydown('Tab'));
            expect(draft.capInputValue).toBe(offered[0]);
            draft.handleCapInputKeydown(keydown('Enter'));

            expect(draft.capabilities).toEqual([offered[0]]);
            expect(draft.capInputValue).toBe('');
        });

        it('starts the walk at the end on a shifted Tab, and adds a typed name on its own', () => {
            const draft = draftOf(storedRow({ sandbox_capabilities: [] }));
            draft.capInputValue = 'CAP_NET';
            const offered = draft.capSuggestions;

            draft.handleCapInputKeydown(keydown('Tab', { shiftKey: true }));
            expect(draft.capInputValue).toBe(offered[offered.length - 1]);

            // A name the list does not carry is still a name the person may type; the server rules
            // on it, the field only normalizes it.
            draft.capInputValue = 'cap_unassigned_9';
            draft.handleCapInputKeydown(keydown('Enter'));
            expect(draft.capabilities).toContain('CAP_UNASSIGNED_9');
        });

        it('closes the dropdown on Escape without changing what is granted', () => {
            const draft = draftOf(storedRow({ sandbox_capabilities: ['CAP_KILL'] }));
            draft.capDropdownOpen = true;

            draft.handleCapInputKeydown(keydown('Escape'));

            expect(draft.capDropdownOpen).toBe(false);
            expect(draft.capabilities).toEqual(['CAP_KILL']);
        });

        it('drops the last name on backspace only when the field is empty', () => {
            const draft = draftOf(storedRow({ sandbox_capabilities: ['CAP_CHOWN', 'CAP_KILL'] }));

            draft.capInputValue = 'x';
            draft.handleCapInputKeydown(keydown('Backspace'));
            expect(draft.capabilities).toHaveLength(2);

            draft.capInputValue = '';
            draft.handleCapInputKeydown(keydown('Backspace'));
            expect(draft.capabilities).toEqual(['CAP_CHOWN']);
        });
    });

    describe('the image build', () => {
        it('starts a build of the stored Dockerfile without writing anything first', async () => {
            const row = storedRow({ sandbox_dockerfile_path: '/tmp/p1/Dockerfile' });
            const draft = draftOf(row);

            await draft.rebuild(row);

            expect(updateProject).not.toHaveBeenCalled();
            expect(buildSandboxImageStream).toHaveBeenCalledWith('p1', expect.any(Function), { noCache: false });
        });

        it('saves a Dockerfile the row does not know about before building it', async () => {
            const row = storedRow({ sandbox_dockerfile_path: '/tmp/p1/Dockerfile.old' });
            const draft = draftOf(row);
            draft.selectDockerfile('/tmp/p1/Dockerfile');
            draft.buildNoCache = true;

            await draft.rebuild(row);

            expect(updateProject).toHaveBeenCalledWith('p1', { sandbox_dockerfile_path: '/tmp/p1/Dockerfile' });
            expect(buildSandboxImageStream.mock.calls[0][2]).toEqual({ noCache: true });
            expect(draft.building).toBe(false);
        });

        it('keeps a build line out of the error line and an error out of the log', async () => {
            const row = storedRow({ sandbox_dockerfile_path: '/tmp/p1/Dockerfile' });
            const draft = draftOf(row);
            buildSandboxImageStream.mockImplementation(async (_id: string, onLine: (line: string) => void) => {
                onLine('STEP 1/2');
                onLine('error: boom');
            });

            await draft.rebuild(row);

            expect(draft.buildLog).toEqual(['STEP 1/2']);
            expect(draft.buildError).toBe('boom');
        });

        it('declines to build twice at once and to build with no Dockerfile', async () => {
            const draft = draftOf(storedRow({ sandbox_dockerfile_path: '' }));
            await draft.rebuild(storedRow());
            expect(buildSandboxImageStream).not.toHaveBeenCalled();

            const row = storedRow({ sandbox_dockerfile_path: '/tmp/p1/Dockerfile' });
            const picked = draftOf(row);
            picked.dockerfilePath = row.sandbox_dockerfile_path ?? '';
            picked.building = true;
            await picked.rebuild(row);
            expect(buildSandboxImageStream).not.toHaveBeenCalled();
        });
    });
});
