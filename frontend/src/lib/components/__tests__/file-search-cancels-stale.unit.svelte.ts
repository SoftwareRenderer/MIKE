// @vitest-environment jsdom
// Several searches can be in flight while a reader types, so only the newest answer may paint — otherwise
// the slowest walk wins and the panel shows a query nobody typed.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, cleanup, act, fireEvent } from '@testing-library/svelte';

const searchFsPaths = vi.fn();

vi.mock('$lib/api', async (importOriginal) => {
    const real = await importOriginal<typeof import('$lib/api')>();
    return { ...real, searchFsPaths: (params: unknown) => searchFsPaths(params) };
});

import FileSearchPanel from '$lib/components/FileSearchPanel.svelte';

// The panel's onDestroy is the other path that drops a walk, and it does not run in this harness.

/** One search the test resolves by hand, with the signal the panel handed down. */
function deferred() {
    let resolve!: (value: unknown) => void;
    let reject!: (reason: unknown) => void;
    const promise = new Promise((res, rej) => {
        resolve = res;
        reject = rej;
    });
    return { promise, resolve, reject };
}

let calls: { query: string; signal?: AbortSignal; done: ReturnType<typeof deferred> }[] = [];

async function type(input: HTMLInputElement, value: string) {
    await fireEvent.input(input, { target: { value } });
    await act(async () => {
        await vi.advanceTimersByTimeAsync(400); // past the panel's 300 ms debounce
    });
}

beforeEach(() => {
    vi.useFakeTimers();
    calls = [];
    searchFsPaths.mockImplementation((params: any) => {
        const done = deferred();
        calls.push({ query: params.query, signal: params.signal, done });
        return done.promise;
    });
});

afterEach(() => {
    cleanup();
    vi.useRealTimers();
});

function panel() {
    const view = render(FileSearchPanel, {
        props: { projectId: 'p1', onFileSelected: () => {}, onClose: () => {} },
    });
    const input = view.container.querySelector('input.search-input') as HTMLInputElement;
    return { ...view, input };
}

describe('FileSearchPanel cancellation', () => {
    it('cancels the walk the next keystroke made irrelevant', async () => {
        const { input } = panel();

        await type(input, 'fir');
        await type(input, 'first');

        expect(calls.map((c) => c.query)).toEqual(['fir', 'first']);
        expect(calls[0].signal?.aborted).toBe(true);
        expect(calls[1].signal?.aborted).toBe(false);
    });

    it('paints the newest answer when an older walk answers last', async () => {
        const { container, input } = panel();

        await type(input, 'old');
        await type(input, 'new');
        expect(calls).toHaveLength(2);

        // The newer search answers first, then the abandoned one catches up.
        calls[1].done.resolve({ root: '/repo', results: [{ path: 'newer.ts', line: 7, text: 'new hit' }] });
        await act(async () => {
            await Promise.resolve();
        });
        expect(container.textContent).toContain('newer.ts');

        calls[0].done.resolve({ root: '/repo', results: [{ path: 'older.ts', line: 1, text: 'stale hit' }] });
        await act(async () => {
            await Promise.resolve();
        });
        expect(container.textContent).not.toContain('older.ts');
        expect(container.textContent).toContain('newer.ts');
    });

    it('drops the answer when the field is emptied', async () => {
        const { container, input } = panel();

        await type(input, 'abc');
        await fireEvent.click(container.querySelector('.clear-btn') as HTMLButtonElement);

        expect(calls[0].signal?.aborted).toBe(true);
        expect(container.querySelector('.search-result-item')).toBeNull();

        // A late answer from the abandoned walk must not repaint what the reader just cleared.
        calls[0].done.resolve({ root: '/repo', results: [{ path: 'hit.ts', line: 1, text: 'x' }] });
        await act(async () => {
            await Promise.resolve();
        });
        expect(container.textContent).not.toContain('hit.ts');
    });

    it('reports nothing when the walk it cancelled comes back refused', async () => {
        const { container, input } = panel();

        await type(input, 'abc');
        await type(input, 'abcdef');

        // An abort can arrive as a refusal of the abandoned request; the reader never asked for it.
        calls[0].done.reject(new Error('aborted'));
        await act(async () => {
            await Promise.resolve();
        });
        expect(container.querySelector('.search-error')).toBeNull();

        calls[1].done.resolve({ root: '/repo', results: [{ path: 'hit.ts', line: 2, text: 'x' }] });
        await act(async () => {
            await Promise.resolve();
        });
        expect(container.textContent).toContain('hit.ts');
        expect(container.querySelector('.search-error')).toBeNull();
    });
});
