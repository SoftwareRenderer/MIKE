import { describe, it, expect, beforeAll, afterEach } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';

import EnvsSection from '$lib/components/SandboxConfigModal/EnvsSection.svelte';
import LockedView from '$lib/components/SandboxConfigModal/LockedView.svelte';
import MountsSection from '$lib/components/SandboxConfigModal/MountsSection.svelte';
import PortsSection from '$lib/components/SandboxConfigModal/PortsSection.svelte';
import SandboxSettingLists from '$lib/components/SandboxSettingLists.svelte';
import { SandboxDraftOwner } from '$lib/components/SandboxConfigModal/draft.svelte';
import type { PortForward } from '$lib/types';

/**
 * A saved row is two halves and nothing between them: the accented half names itself, so any glyph added
 * back between them changes the row's text — which is what these assertions read.
 */

beforeAll(() => {
    // jsdom has no Web Animations API, and the slide transition calls it when a section opens.
    if (!Element.prototype.animate) {
        Element.prototype.animate = () =>
            ({ finished: Promise.resolve(), cancel() { }, play() { }, pause() { }, commit() { }, onfinish: null }) as unknown as Animation;
    }
});

afterEach(cleanup);

function one<T extends Element>(container: HTMLElement, selector: string): T {
    const el = container.querySelector(selector);
    if (!el) throw new Error(`not found: ${selector}`);
    return el as T;
}

/** A row's text as it is painted: whitespace between two flex halves is not rendered, so only the glyphs
 *  a row actually writes are compared. */
function visible(el: Element): string {
    return (el.textContent ?? '').replace(/\s+/g, '');
}

function mountDraft(host_path: string, container_path: string) {
    const draft = new SandboxDraftOwner();
    draft.sandboxEnabled = true;
    draft.mountsExpanded = true;
    draft.mounts = [{ host_path, container_path, mode: 'rw' }];
    return draft;
}

function envDraft(key: string, value: string) {
    const draft = new SandboxDraftOwner();
    draft.sandboxEnabled = true;
    draft.envsExpanded = true;
    draft.envs = [{ key, value }];
    return draft;
}

function portDraft(ports: PortForward[], assigned: Map<number, number> = new Map()) {
    const draft = new SandboxDraftOwner();
    draft.sandboxEnabled = true;
    draft.portsExpanded = true;
    draft.forwardPorts = ports;
    draft.activeHostPorts = assigned;
    return draft;
}

describe('a saved pair', () => {
    it('holds a mount as its two paths and nothing between them', () => {
        const { container } = render(MountsSection, {
            props: { draft: mountDraft('/srv/datasets', '/mnt/datasets'), locked: false, onBrowse: () => { } },
        });
        // The rw / ro chip is part of a mount row's text here and in the read-only lists, so it is part of
        // both comparisons.
        expect(visible(one(container, '.mount-item'))).toBe('/srv/datasets/mnt/datasetsrw');
    });

    it('holds a variable as its name and its value and nothing between them', () => {
        const { container } = render(EnvsSection, {
            props: { draft: envDraft('CARGO_HOME', '/srv/cargo'), locked: false },
        });
        expect(visible(one(container, '.env-item'))).toBe('CARGO_HOME/srv/cargo');
    });

    it('reads the same way in the read-only lists, so no screen re-adds a separator', () => {
        const { container } = render(SandboxSettingLists, {
            props: {
                mounts: [{ host_path: '/srv/datasets', container_path: '/mnt/datasets', mode: 'ro' as const }],
                envs: [{ key: 'CARGO_HOME', value: '/srv/cargo' }],
            },
        });
        const items = [...container.querySelectorAll('.sandbox-item')];
        expect(visible(items[0])).toBe('/srv/datasets/mnt/datasetsro');
        expect(visible(items[1])).toBe('CARGO_HOME/srv/cargo');
    });
});

describe('a port mapping, which is short enough to be one token', () => {
    const row = (c: HTMLElement) => visible(one(c, '.port-item'));

    it('reads host:container while a task publishes it', () => {
        const { container } = render(PortsSection, {
            props: { draft: portDraft([{ container_port: 3000 }], new Map([[3000, 32789]])), locked: false },
        });
        expect(row(container)).toBe('32789:3000');
    });

    it('puts the chosen interface in front of the host port, never in front of the container port', () => {
        const { container } = render(PortsSection, {
            props: {
                draft: portDraft([{ container_port: 3000, host_ip: '127.0.0.1' }], new Map([[3000, 32789]])),
                locked: false,
            },
        });
        expect(row(container)).toBe('127.0.0.1:32789:3000');
    });

    it('shows the container port alone until something publishes it', () => {
        const { container } = render(PortsSection, {
            props: { draft: portDraft([{ container_port: 3000 }]), locked: false },
        });
        expect(row(container)).toBe('3000');
    });

    it('keeps the interface with the container port when no host port exists to carry it', () => {
        const { container } = render(PortsSection, {
            props: { draft: portDraft([{ container_port: 3000, host_ip: '127.0.0.1' }]), locked: false },
        });
        expect(row(container)).toBe('127.0.0.1:3000');
    });

    it('reads the same way while the screen is locked to a running task', () => {
        const { container } = render(LockedView, {
            props: { draft: portDraft([{ container_port: 3000 }], new Map([[3000, 32789]])) },
        });
        expect(visible(one(container, '.sandbox-item-pair'))).toBe('32789:3000');
    });
});
