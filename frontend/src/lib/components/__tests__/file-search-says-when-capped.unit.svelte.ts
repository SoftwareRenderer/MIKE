// @vitest-environment jsdom
// 1000 matches and 1000+ are different claims, and only the server knows which one it answered.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, cleanup, act, fireEvent } from '@testing-library/svelte';

const searchFsPaths = vi.fn();

vi.mock('$lib/api', async (importOriginal) => {
    const real = await importOriginal<typeof import('$lib/api')>();
    return { ...real, searchFsPaths: (params: unknown) => searchFsPaths(params) };
});

import FileSearchPanel from '$lib/components/FileSearchPanel.svelte';

const rows = (n: number) => Array.from({ length: n }, (_, i) => ({ path: `f${i}.go`, line: i + 1, text: 'hit' }));

async function searchWith(answer: { root: string; results: unknown[]; truncated?: boolean }) {
    const view = render(FileSearchPanel, {
        props: { projectId: 'p1', onFileSelected: () => {}, onClose: () => {} },
    });
    searchFsPaths.mockResolvedValue(answer);
    const input = view.container.querySelector('input.search-input') as HTMLInputElement;
    await fireEvent.input(input, { target: { value: 'hit' } });
    await act(async () => {
        await vi.advanceTimersByTimeAsync(400); // past the panel's 300 ms debounce
    });
    await act(async () => {
        await Promise.resolve();
    });
    return view.container.querySelector('.match-count')?.textContent?.trim() ?? '';
}

beforeEach(() => {
    vi.useFakeTimers();
});

afterEach(() => {
    cleanup();
    vi.useRealTimers();
});

describe('the result count', () => {
    it('counts a walk that reached the end of the tree', async () => {
        expect(await searchWith({ root: '/repo', results: rows(2) })).toBe('2 results');
    });

    it('marks an answer the ceiling stopped', async () => {
        expect(await searchWith({ root: '/repo', results: rows(2), truncated: true })).toBe('2+ results');
    });
});
