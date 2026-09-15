// @vitest-environment jsdom
// The promise-dialog rule: who may empty the slot, what each answer resolves, and which dialog
// hears Enter. (`.svelte.ts` extension: the composable under test uses `$state` and `$effect`.)
//
// The unmount safety net is not covered here: the suite's setup stubs `onDestroy`, so no test can
// observe what runs when a component is destroyed.
import { describe, it, expect, afterEach } from 'vitest';
import { render, cleanup, fireEvent } from '@testing-library/svelte';
import Probe from './PromiseDialogProbe.svelte';

interface Request {
    text: string;
    _resolve?: (value: boolean) => void;
}

/**
 * The pending-request slots as `$state` fields on a class — the shape `DialogStore` has, so the
 * arming effect follows a real store write rather than a plain object.
 */
class Slots {
    a = $state<Request | null>(null);
    b = $state<Request | null>(null);
}

async function flush() {
    for (let i = 0; i < 6; i++) await Promise.resolve();
}

function slotOf(store: Slots, key: 'a' | 'b') {
    return { get: () => store[key], clear: () => { store[key] = null; } };
}

/** Arm a request in one slot and hand back the promise its caller is waiting on. */
function arm(store: Slots, key: 'a' | 'b', text: string): Promise<boolean> {
    return new Promise((resolve) => {
        store[key] = { text, _resolve: resolve };
    });
}

const box = (container: HTMLElement, label: string) =>
    container.querySelector(`.dialog-${label}`) as HTMLElement | null;
const button = (container: HTMLElement, label: string, which: 'confirm' | 'cancel') =>
    box(container, label)!.querySelector(`.${which}`) as HTMLElement;

afterEach(() => {
    cleanup();
    document.body.innerHTML = '';
});

describe('usePromiseDialog', () => {
    it('arms from the slot, and a cancel clears that slot and resolves false', async () => {
        const store = new Slots();
        const { container } = render(Probe, { props: { slot: slotOf(store, 'a'), label: 'a' } });
        const answered = arm(store, 'a', 'first');
        await flush();

        expect(box(container, 'a')?.textContent).toContain('first');
        expect(store.a).not.toBeNull();

        await fireEvent.click(button(container, 'a', 'cancel'));
        await flush();

        expect(store.a).toBeNull();
        expect(box(container, 'a')).toBeNull();
        await expect(answered).resolves.toBe(false);
    });

    it('a confirm resolves true', async () => {
        const store = new Slots();
        const { container } = render(Probe, { props: { slot: slotOf(store, 'a'), label: 'a' } });
        const answered = arm(store, 'a', 'ship it');
        await flush();

        await fireEvent.click(button(container, 'a', 'confirm'));
        await expect(answered).resolves.toBe(true);
    });

    it('each dialog empties only the slot it armed from', async () => {
        const store = new Slots();
        const first = render(Probe, { props: { slot: slotOf(store, 'a'), label: 'a' } });
        const second = render(Probe, { props: { slot: slotOf(store, 'b'), label: 'b' } });
        const firstAnswer = arm(store, 'a', 'one');
        const secondAnswer = arm(store, 'b', 'two');
        await flush();

        await fireEvent.click(button(first.container, 'a', 'cancel'));
        await flush();

        expect(store.a).toBeNull();
        // The other request is still pending, and its dialog is still open on it.
        expect(store.b?.text).toBe('two');
        expect(box(second.container, 'b')).not.toBeNull();
        await expect(firstAnswer).resolves.toBe(false);
        await fireEvent.click(button(second.container, 'b', 'confirm'));
        await expect(secondAnswer).resolves.toBe(true);
    });

    it('Enter confirms while a request is pending', async () => {
        const store = new Slots();
        render(Probe, { props: { slot: slotOf(store, 'a'), label: 'a' } });
        const answered = arm(store, 'a', 'enter me');
        await flush();

        await fireEvent.keyDown(window, { key: 'Enter' });
        await flush();

        expect(store.a).toBeNull();
        await expect(answered).resolves.toBe(true);
    });

    it('Enter answers nothing while the slot is empty', async () => {
        const store = new Slots();
        render(Probe, { props: { slot: slotOf(store, 'a'), label: 'a' } });
        await flush();

        await fireEvent.keyDown(window, { key: 'Enter' });
        await flush();

        expect(store.a).toBeNull();
    });

    it('leaves Enter alone when confirmOnEnter is off', async () => {
        const store = new Slots();
        render(Probe, { props: { slot: slotOf(store, 'a'), label: 'a', confirmOnEnter: false } });
        arm(store, 'a', 'stay');
        await flush();

        await fireEvent.keyDown(window, { key: 'Enter' });
        await flush();

        expect(store.a?.text).toBe('stay');
    });

    it('arms again with the next request after a dismiss', async () => {
        const store = new Slots();
        const { container } = render(Probe, { props: { slot: slotOf(store, 'a'), label: 'a' } });
        const first = arm(store, 'a', 'first');
        await flush();
        await fireEvent.click(button(container, 'a', 'cancel'));
        await first;
        await flush();

        const second = arm(store, 'a', 'second');
        await flush();

        expect(box(container, 'a')?.textContent).toContain('second');
        await fireEvent.click(button(container, 'a', 'confirm'));
        await expect(second).resolves.toBe(true);
        expect(store.a).toBeNull();
    });
});
