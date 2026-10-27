import { describe, it, expect } from 'vitest';
import { restoreScrollTop, restoresSavedOffset } from '../editor-scroll';

/**
 * A pane whose geometry is state the test moves, because jsdom lays nothing out. `scrollTop` clamps the way a
 * browser clamps it, so a write against an unmeasured pane is lost here the way it is lost there.
 */
function makeScroller(initial: { clientHeight?: number; scrollHeight?: number } = {}) {
    const el = document.createElement('div');
    const state = {
        clientHeight: initial.clientHeight ?? 600,
        scrollHeight: initial.scrollHeight ?? 8000,
        scrollTop: 0,
        writes: 0,
    };
    Object.defineProperty(el, 'clientHeight', { configurable: true, get: () => state.clientHeight });
    Object.defineProperty(el, 'scrollHeight', { configurable: true, get: () => state.scrollHeight });
    Object.defineProperty(el, 'scrollTop', {
        configurable: true,
        get: () => state.scrollTop,
        set: (value: number) => {
            state.writes++;
            state.scrollTop = Math.max(0, Math.min(value, state.scrollHeight - state.clientHeight));
        },
    });
    return { el, state };
}

async function frames(n: number) {
    for (let i = 0; i < n; i++) await new Promise((r) => requestAnimationFrame(() => r(null)));
}

describe('restoreScrollTop', () => {
    it('lands the offset on a pane that is already measured', async () => {
        const { el, state } = makeScroller();

        restoreScrollTop(() => el, 2000);
        await frames(3);

        expect(state.scrollTop).toBe(2000);
        expect(state.writes).toBe(1);
    });

    it('waits out a pane that has no height yet instead of landing at the top', async () => {
        const { el, state } = makeScroller({ clientHeight: 0, scrollHeight: 0 });

        restoreScrollTop(() => el, 2000);
        await frames(4);
        expect(state.scrollTop).toBe(0);
        expect(state.writes).toBe(0); // nothing written while the pane could not hold the offset

        state.clientHeight = 600; // the tab became the active one
        state.scrollHeight = 8000;
        await frames(4);

        expect(state.scrollTop).toBe(2000);
    });

    it('lands the offset on a pane that exists only after the wait began', async () => {
        const late = makeScroller();
        let pane: HTMLElement | null = null;

        restoreScrollTop(() => pane, 2000);
        await frames(4);

        pane = late.el;
        await frames(4);

        expect(late.state.scrollTop).toBe(2000);
    });

    it('re-writes when the document measures taller than the first write knew', async () => {
        const { el, state } = makeScroller({ scrollHeight: 1800 });

        restoreScrollTop(() => el, 2000);
        await frames(2);
        expect(state.scrollTop).toBe(1200); // clamped to what it could reach

        state.scrollHeight = 8000; // the lines above the target realized their height
        await frames(4);

        expect(state.scrollTop).toBe(2000);
    });

    it('stops by itself once the offset has landed', async () => {
        const { el, state } = makeScroller();

        restoreScrollTop(() => el, 2000);
        await frames(2);
        const writes = state.writes;
        await frames(10);

        expect(state.writes).toBe(writes);
    });

    it('holds the offset while the pane keeps moving it', async () => {
        const { el, state } = makeScroller();

        restoreScrollTop(() => el, 2000);
        await frames(1);
        expect(state.scrollTop).toBe(2000);

        state.scrollTop = 3001;
        await frames(2);

        expect(state.scrollTop).toBe(2000);
    });

    it('gives the pane back once it has finished measuring', async () => {
        const { el, state } = makeScroller();

        restoreScrollTop(() => el, 2000);
        await frames(40); // past the settle window, whatever it is sized at

        state.scrollTop = 3001;
        await frames(4);

        expect(state.scrollTop).toBe(3001);
    });

    it('leaves a view the reader moved before the offset was known', async () => {
        const { el, state } = makeScroller();
        state.scrollTop = 640;

        restoreScrollTop(() => el, 2000);
        await frames(4);

        expect(state.scrollTop).toBe(640);
        expect(state.writes).toBe(0);
    });

    it('does nothing when there is no offset to restore', async () => {
        const { el, state } = makeScroller();

        restoreScrollTop(() => el, 0);
        await frames(2);

        expect(state.writes).toBe(0);
    });
});

describe('restoresSavedOffset', () => {
    const pane = { top: 2000, targetLine: 0, mode: 'explorer' };

    it('restores the offset of a plain explorer reopen', () => {
        expect(restoresSavedOffset(pane)).toBe(true);
    });

    it('gives the scroll to a line jump instead of the saved offset', () => {
        expect(restoresSavedOffset({ ...pane, targetLine: 1 })).toBe(false);
        expect(restoresSavedOffset({ ...pane, targetLine: 128 })).toBe(false);
    });

    it('refuses when there is nothing to restore, or the pane is not an explorer pane', () => {
        expect(restoresSavedOffset({ ...pane, top: 0 })).toBe(false);
        expect(restoresSavedOffset({ ...pane, mode: 'diff' })).toBe(false);
    });
});
