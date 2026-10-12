import { describe, it, expect, beforeAll, afterEach } from 'vitest';
import { render, cleanup, fireEvent } from '@testing-library/svelte';

import CapabilitiesSection from '$lib/components/SandboxConfigModal/CapabilitiesSection.svelte';
import { SandboxDraftOwner } from '$lib/components/SandboxConfigModal/draft.svelte';
import { DOCKER_DEFAULT_CAPABILITIES } from '$lib/components/SandboxConfigModal/capabilities';
import type { Project } from '$lib/types';

/** What only a render answers: the control is on the row, one click paints the set, and both disabled states hold. */

beforeAll(() => {
    // jsdom has no Web Animations API, and the slide transition calls it when a section opens.
    if (!Element.prototype.animate) {
        Element.prototype.animate = () =>
            ({ finished: Promise.resolve(), cancel() { }, play() { }, pause() { }, commit() { }, onfinish: null }) as unknown as Animation;
    }
});

afterEach(cleanup);

function storedRow(capabilities: string[]): Project {
    return {
        id: 'p1',
        name: 'Test Project',
        path: '/tmp/p1',
        sandbox_enabled: true,
        sandbox_capabilities: capabilities,
    } as Project;
}

function section(capabilities: string[], locked = false) {
    const draft = new SandboxDraftOwner();
    draft.reseed(storedRow(capabilities));
    draft.capsExpanded = true;
    return render(CapabilitiesSection, { props: { draft, locked } });
}

function one(container: HTMLElement): HTMLButtonElement {
    const el = container.querySelector<HTMLButtonElement>('.caps-defaults');
    if (!el) throw new Error('defaults shortcut not found');
    return el;
}

function tagNames(container: HTMLElement): string[] {
    return [...container.querySelectorAll('.caps-tag-text')].map(el => el.textContent ?? '');
}

describe('the capability defaults shortcut', () => {
    it('grants every default with one click', async () => {
        const { container } = section([]);

        expect(one(container).disabled).toBe(false);
        await fireEvent.click(one(container));

        expect(tagNames(container)).toEqual([...DOCKER_DEFAULT_CAPABILITIES]);
    });

    it('is disabled once every default is already granted', () => {
        const { container } = section([...DOCKER_DEFAULT_CAPABILITIES]);

        expect(one(container).disabled).toBe(true);
    });

    it('is disabled while the form cannot be edited', () => {
        const { container } = section(['CAP_KILL'], true);

        expect(one(container).disabled).toBe(true);
    });
});
