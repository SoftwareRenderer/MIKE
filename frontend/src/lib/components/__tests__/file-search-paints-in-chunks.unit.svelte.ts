// @vitest-environment jsdom
// Painting all 1000 rows costs real time, so the panel puts down a chunk and offers the rest.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, cleanup, act, fireEvent } from '@testing-library/svelte';

const searchFsPaths = vi.fn();

vi.mock('$lib/api', async (importOriginal) => {
    const real = await importOriginal<typeof import('$lib/api')>();
    return { ...real, searchFsPaths: (params: unknown) => searchFsPaths(params) };
});

import FileSearchPanel from '$lib/components/FileSearchPanel.svelte';

const rows = (n: number) =>
    Array.from({ length: n }, (_, i) => ({ path: `src/f${i % 20}.ts`, line: i + 1, text: `hit ${i}` }));

async function paint(n: number) {
    const view = render(FileSearchPanel, {
        props: { projectId: 'p1', onFileSelected: () => {}, onClose: () => {} },
    });
    searchFsPaths.mockResolvedValue({ root: '/repo', results: rows(n) });
    const input = view.container.querySelector('input.search-input') as HTMLInputElement;
    await fireEvent.input(input, { target: { value: 'hit' } });
    await fireEvent.keyDown(input, { key: 'Enter' }); // Enter runs the search now, skipping the debounce
    await act(async () => {
        await new Promise((r) => setTimeout(r, 0));
    });
    return view;
}

const shown = (container: HTMLElement) => container.querySelectorAll('.search-result-item').length;

beforeEach(() => {
    vi.useRealTimers();
});

afterEach(() => {
    cleanup();
});

describe('the result paint', () => {
    it('puts down one chunk when the answer is larger', async () => {
        const view = await paint(300);
        expect(shown(view.container)).toBe(200);
        const more = view.container.querySelector('.show-more');
        expect(more?.textContent?.trim()).toBe('Show 100 more');
    });

    it('gives the rest when asked, and stops offering it', async () => {
        const view = await paint(300);
        const more = view.container.querySelector('.show-more') as HTMLButtonElement;
        await fireEvent.click(more);
        expect(shown(view.container)).toBe(300);
        expect(view.container.querySelector('.show-more')).toBeNull();
    });

    it('offers no button when everything already fits', async () => {
        const view = await paint(50);
        expect(shown(view.container)).toBe(50);
        expect(view.container.querySelector('.show-more')).toBeNull();
    });
});
