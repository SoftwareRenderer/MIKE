import { describe, it, expect, beforeAll, afterEach } from 'vitest';
import { render, cleanup, fireEvent } from '@testing-library/svelte';

import MountsSection from '$lib/components/SandboxConfigModal/MountsSection.svelte';
import { SandboxDraftOwner } from '$lib/components/SandboxConfigModal/draft.svelte';

beforeAll(() => {
    // jsdom has no Web Animations API, and the slide transition calls it when a section opens.
    if (!Element.prototype.animate) {
        Element.prototype.animate = () =>
            ({ finished: Promise.resolve(), cancel() { }, play() { }, pause() { }, commit() { }, onfinish: null }) as unknown as Animation;
    }
});

afterEach(cleanup);

function draftWith(...mounts: { host_path: string; container_path: string; mode: 'rw' | 'ro' }[]) {
    const draft = new SandboxDraftOwner();
    draft.sandboxEnabled = true;
    draft.mountsExpanded = true;
    draft.mounts = mounts;
    return draft;
}

function one<T extends Element>(container: HTMLElement, selector: string): T {
    const el = container.querySelector(selector);
    if (!el) throw new Error(`not found: ${selector}`);
    return el as T;
}

const hostInput = (c: HTMLElement) => one<HTMLInputElement>(c, 'input.mount-input-host');
const containerInput = (c: HTMLElement) => one<HTMLInputElement>(c, '.mount-input:not(.mount-input-host)');

describe('the mount row form', () => {
    it('tells every keyboard a path field holds text to reproduce literally', () => {
        const { container } = render(MountsSection, { props: { draft: draftWith(), locked: false, onBrowse: () => { } } });
        for (const field of [hostInput(container), containerInput(container)]) {
            expect(field.getAttribute('spellcheck')).toBe('false');
            expect(field.getAttribute('autocomplete')).toBe('off');
            expect(field.getAttribute('autocorrect')).toBe('off');
            expect(field.getAttribute('autocapitalize')).toBe('none');
            expect(field.getAttribute('enterkeyhint')).toBe('done');
        }
    });

    it('adds the row when the keyboard commits it, since a phone has no other way', async () => {
        const draft = draftWith();
        const { container } = render(MountsSection, { props: { draft, locked: false, onBrowse: () => { } } });
        await fireEvent.input(hostInput(container), { target: { value: '/srv/data' } });
        await fireEvent.input(containerInput(container), { target: { value: '/mnt/data' } });
        await fireEvent.keyDown(containerInput(container), { key: 'Enter' });
        expect(draft.mounts).toEqual([{ host_path: '/srv/data', container_path: '/mnt/data', mode: 'rw' }]);
    });

    it('opens a saved row on Space, which role="button" promises', async () => {
        const draft = draftWith({ host_path: '/srv/data', container_path: '/mnt/data', mode: 'rw' });
        const { container } = render(MountsSection, { props: { draft, locked: false, onBrowse: () => { } } });
        await fireEvent.keyDown(one(container, '.mount-item'), { key: ' ' });
        expect(draft.editingMountIndex).toBe(0);
        expect(containerInput(container).value).toBe('/mnt/data');
    });

    it('brings the fields into view when a row opens, because they sit under the list', async () => {
        const draft = draftWith({ host_path: '/srv/data', container_path: '/mnt/data', mode: 'rw' });
        const { container } = render(MountsSection, { props: { draft, locked: false, onBrowse: () => { } } });
        const form = one(container, '.mount-add-row');
        let scrolled: string | null = null;
        form.scrollIntoView = () => { scrolled = 'called'; };
        await fireEvent.click(one(container, '.mount-item'));
        await new Promise(r => requestAnimationFrame(r));
        expect(scrolled).toBe('called');
    });
});
