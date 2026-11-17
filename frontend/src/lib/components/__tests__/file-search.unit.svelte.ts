// @vitest-environment jsdom
/**
 * THE FILE SEARCH.
 *
 * Several walks can be in flight while a reader types, so only the newest answer may paint — otherwise
 * the slowest walk wins and the panel shows a query nobody typed (its `onDestroy` is the other path that
 * drops a walk, and it does not run in this harness). A large answer is put down in chunks rather than
 * in one long frame. A walk that hit the server's ceiling and a walk that finished the tree are
 * different claims, and only the answer knows which one it is. A matched line arrives from the server,
 * so its bytes reach the row as text or not at all.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, cleanup, act, fireEvent } from '@testing-library/svelte';

const searchFsPaths = vi.fn();

vi.mock('$lib/api', async (importOriginal) => {
    const real = await importOriginal<typeof import('$lib/api')>();
    return { ...real, searchFsPaths: (params: unknown) => searchFsPaths(params) };
});

import FileSearchPanel from '$lib/components/FileSearchPanel.svelte';

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

function panel() {
    const view = render(FileSearchPanel, {
        props: { projectId: 'p1', onFileSelected: () => {}, onClose: () => {} },
    });
    const input = view.container.querySelector('input.search-input') as HTMLInputElement;
    return { ...view, input };
}

const shown = (container: HTMLElement) => container.querySelectorAll('.search-result-item').length;

/** Type, and wait out the panel's 300 ms debounce. */
async function type(input: HTMLInputElement, value: string) {
    await fireEvent.input(input, { target: { value } });
    await act(async () => {
        await vi.advanceTimersByTimeAsync(400);
    });
}

/** Let the answer that is already in flight land. */
async function settle() {
    await act(async () => {
        await Promise.resolve();
    });
}

/** Answer the next search with `results`, run it, and hand back what the panel says about the count. */
async function counted(answer: { root: string; results: unknown[]; truncated?: boolean }): Promise<string> {
    searchFsPaths.mockResolvedValue(answer);
    const { container, input } = panel();
    await type(input, 'hit');
    await settle();
    return container.querySelector('.match-count')?.textContent?.trim() ?? '';
}

beforeEach(() => {
    calls = [];
    searchFsPaths.mockReset();
});

afterEach(() => {
    cleanup();
    vi.useRealTimers();
});

describe('FileSearchPanel cancellation', () => {
    beforeEach(() => {
        vi.useFakeTimers();
        searchFsPaths.mockImplementation((params: any) => {
            const done = deferred();
            calls.push({ query: params.query, signal: params.signal, done });
            return done.promise;
        });
    });

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
        await settle();
        expect(container.textContent).toContain('newer.ts');

        calls[0].done.resolve({ root: '/repo', results: [{ path: 'older.ts', line: 1, text: 'stale hit' }] });
        await settle();
        expect(container.textContent).not.toContain('older.ts');
        expect(container.textContent).toContain('newer.ts');
    });

    it('drops the answer when the field is emptied', async () => {
        const { container, input } = panel();

        await type(input, 'abc');
        await fireEvent.click(container.querySelector('.clear-btn') as HTMLButtonElement);

        expect(calls[0].signal?.aborted).toBe(true);
        expect(shown(container)).toBe(0);

        // A late answer from the abandoned walk must not repaint what the reader just cleared.
        calls[0].done.resolve({ root: '/repo', results: [{ path: 'hit.ts', line: 1, text: 'x' }] });
        await settle();
        expect(container.textContent).not.toContain('hit.ts');
    });

    it('reports nothing when the walk it cancelled comes back refused', async () => {
        const { container, input } = panel();

        await type(input, 'abc');
        await type(input, 'abcdef');

        // An abort can arrive as a refusal of the abandoned request; the reader never asked for it.
        calls[0].done.reject(new Error('aborted'));
        await settle();
        expect(container.querySelector('.search-error')).toBeNull();

        calls[1].done.resolve({ root: '/repo', results: [{ path: 'hit.ts', line: 2, text: 'x' }] });
        await settle();
        expect(container.textContent).toContain('hit.ts');
        expect(container.querySelector('.search-error')).toBeNull();
    });
});

describe('the result paint', () => {
    const rows = (n: number) =>
        Array.from({ length: n }, (_, i) => ({ path: `src/f${i % 20}.ts`, line: i + 1, text: `hit ${i}` }));

    /** A real answer of `n` rows, run now (Enter skips the debounce) so the paint is on screen. */
    async function paint(n: number) {
        searchFsPaths.mockResolvedValue({ root: '/repo', results: rows(n) });
        const { container, input } = panel();
        await fireEvent.input(input, { target: { value: 'hit' } });
        await fireEvent.keyDown(input, { key: 'Enter' });
        await act(async () => {
            await new Promise((r) => setTimeout(r, 0));
        });
        return container;
    }

    it.each([
        { answer: 300, painted: 200, more: 'Show 100 more' },
        { answer: 50, painted: 50, more: null },
    ])('puts down $painted rows of a $answer-row answer', async ({ answer, painted, more }) => {
        const container = await paint(answer);

        expect(shown(container)).toBe(painted);
        expect(container.querySelector('.show-more')?.textContent?.trim() ?? null).toBe(more);
    });

    it('gives the rest when asked, and stops offering it', async () => {
        const container = await paint(300);
        const more = container.querySelector('.show-more') as HTMLButtonElement;

        await fireEvent.click(more);

        expect(shown(container)).toBe(300);
        expect(container.querySelector('.show-more')).toBeNull();
    });
});

describe('the answer, counted and highlighted', () => {
    beforeEach(() => vi.useFakeTimers());

    const rows = (n: number) => Array.from({ length: n }, (_, i) => ({ path: `f${i}.go`, line: i + 1, text: 'hit' }));

    it.each([
        { name: 'a walk that reached the end of the tree', answer: { root: '/repo', results: rows(2) }, count: '2 results' },
        { name: 'an answer the ceiling stopped', answer: { root: '/repo', results: rows(2), truncated: true }, count: '2+ results' },
    ])('counts $name as $count', async ({ answer, count }) => {
        expect(await counted(answer)).toBe(count);
    });

    it('paints the match as a mark and every other byte as text', async () => {
        searchFsPaths.mockResolvedValue({
            root: '/repo',
            results: [{ path: 'a.go', line: 1, text: 'call <script>alert("x")</script> hit' }],
        });
        const { container, input } = panel();
        await type(input, 'hit');
        await settle();

        // The row is {@html}, so an answer that holds markup reaches it as text or not at all.
        const line = container.querySelector('.line-text') as HTMLElement;
        expect(line.querySelector('script')).toBeNull();
        expect(line.textContent).toBe('call <script>alert("x")</script> hit');
        expect(line.querySelector('mark')?.textContent).toBe('hit');
    });
});
