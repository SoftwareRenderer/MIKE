import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';

// Mock raw asset imports and the icon component (not available in jsdom)
vi.mock('$lib/assets/icons/shield.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/shield-off.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/home.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/folder.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/file.svg?raw', () => ({ default: '' }));
vi.mock('$lib/components/Icon.svelte', () => ({ default: () => '' }));

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
import type { PortForward, Project } from '$lib/types';

function projectWith(ports: PortForward[]): Project {
    return {
        id: 'p1',
        name: 'Test Project',
        path: '/tmp/p1',
        sandbox_enabled: true,
        sandbox_network_isolation: false,
        sandbox_memory_limit: 1024,
        forward_ports: ports,
    } as Project;
}

function renderModal(project: Project) {
    return render(SandboxConfigModal, {
        props: { project, inProgressCount: 0, onClose: () => { }, onSaved: () => { } },
    });
}

function one<T extends Element>(container: HTMLElement, selector: string): T {
    const el = container.querySelector(selector);
    if (!el) throw new Error(`not found: ${selector}`);
    return el as T;
}

const interfaceInput = (c: HTMLElement) => one<HTMLInputElement>(c, 'input[aria-label="Host interface"]');
const containerPortInput = (c: HTMLElement) => one<HTMLInputElement>(c, 'input[aria-label="Container port"]');
const portItems = (c: HTMLElement) => Array.from(c.querySelectorAll<HTMLElement>('.port-item'));

async function expandPorts(container: HTMLElement) {
    const toggle = one<HTMLButtonElement>(container, 'button[aria-label="Toggle port forwarding settings"]');
    if (toggle.getAttribute('aria-expanded') !== 'true') await fireEvent.click(toggle);
}

async function addPort(container: HTMLElement, containerPort: string, hostIP: string) {
    await fireEvent.input(containerPortInput(container), { target: { value: containerPort } });
    await fireEvent.input(interfaceInput(container), { target: { value: hostIP } });
    await fireEvent.click(one<HTMLButtonElement>(container, '.btn-add-port'));
}

/** Save and hand back what the modal sent. */
async function save(container: HTMLElement): Promise<Record<string, unknown>> {
    await fireEvent.click(one<HTMLButtonElement>(container, '.btn-primary'));
    await waitFor(() => expect(updateProject).toHaveBeenCalled());
    return updateProject.mock.calls[0][1] as Record<string, unknown>;
}

describe('SandboxConfigModal forwarded ports', () => {
    beforeAll(() => {
        // jsdom has no Web Animations API, and the slide transition calls it when a block
        // enters - which adding or removing a row does.
        if (!Element.prototype.animate) {
            Element.prototype.animate = () =>
                ({ finished: Promise.resolve(), cancel() { }, play() { }, pause() { }, commit() { }, onfinish: null }) as unknown as Animation;
        }
    });

    beforeEach(() => {
        updateProject.mockReset().mockResolvedValue({});
        getProject.mockResolvedValue({ id: 'p1', active_terminal_ports: [] });
        getBoard.mockResolvedValue({ tasks: [] });
    });

    afterEach(() => cleanup());

    it('prints the interface only where one was chosen', async () => {
        const { container } = renderModal(projectWith([{ container_port: 3000 }, { container_port: 80, host_ip: '192.168.1.100' }]));
        await expandPorts(container);

        const rows = portItems(container).map(el => el.textContent ?? '');
        expect(rows).toHaveLength(2);
        expect(rows[0]).toContain('3000');
        expect(rows[0]).not.toContain('0.0.0.0');
        expect(rows[1]).toContain('192.168.1.100:80');
    });

    it('saves a port bound to one interface', async () => {
        const { container } = renderModal(projectWith([]));
        await expandPorts(container);
        await addPort(container, '3000', '192.168.1.100');

        expect(portItems(container)).toHaveLength(1);
        expect(await save(container)).toMatchObject({
            sandbox_forward_ports: [{ container_port: 3000, host_ip: '192.168.1.100' }],
        });
    });

    it('leaves the interface out when every interface is meant', async () => {
        const { container } = renderModal(projectWith([]));
        await expandPorts(container);
        await addPort(container, '3000', '');

        const changes = await save(container);
        const ports = changes.sandbox_forward_ports as PortForward[];
        expect(ports).toHaveLength(1);
        expect(ports[0].container_port).toBe(3000);
        expect(ports[0]).not.toHaveProperty('host_ip');
    });

    it('normalizes an explicit wildcard to every interface', async () => {
        const { container } = renderModal(projectWith([]));
        await expandPorts(container);
        await addPort(container, '3000', '0.0.0.0');

        const ports = (await save(container)).sandbox_forward_ports as PortForward[];
        expect(ports[0]).not.toHaveProperty('host_ip');
    });

    it('refuses an interface that is not an IP address and adds nothing', async () => {
        const { container } = renderModal(projectWith([]));
        await expandPorts(container);
        await addPort(container, '3000', 'saturn.lan');

        expect(one<HTMLElement>(container, '.port-error').textContent).toContain('IP address');
        expect(portItems(container)).toHaveLength(0);

        // Nothing changed, so there is nothing to send.
        await fireEvent.click(one<HTMLButtonElement>(container, '.btn-primary'));
        await new Promise(resolve => setTimeout(resolve, 0));
        expect(updateProject).not.toHaveBeenCalled();
    });

    it('sends a change to the interface alone', async () => {
        const { container } = renderModal(projectWith([{ container_port: 3000 }]));
        await expandPorts(container);

        await fireEvent.click(portItems(container)[0]);
        expect(interfaceInput(container).value).toBe('0.0.0.0');
        await fireEvent.input(interfaceInput(container), { target: { value: '192.168.1.100' } });
        await fireEvent.click(one<HTMLButtonElement>(container, '.btn-add-port'));

        expect(await save(container)).toMatchObject({
            sandbox_forward_ports: [{ container_port: 3000, host_ip: '192.168.1.100' }],
        });
    });

    it('sends a removal', async () => {
        const { container } = renderModal(projectWith([{ container_port: 3000 }]));
        await expandPorts(container);

        await fireEvent.click(one<HTMLButtonElement>(container, '.btn-remove-port'));
        expect(portItems(container)).toHaveLength(0);

        expect(await save(container)).toMatchObject({ sandbox_forward_ports: [] });
    });
});
