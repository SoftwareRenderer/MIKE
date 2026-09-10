import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, cleanup, waitFor, fireEvent } from '@testing-library/svelte';

vi.mock('$lib/storage', () => ({
    loadAppSetting: vi.fn(async (key: string, fallback: unknown) =>
        key === 'symbols-tree-view' ? true : fallback),
    saveAppSetting: vi.fn(async () => {}),
}));

import BranchSymbols from '$lib/components/BranchSymbols.svelte';

const SYMBOLS = [
    {
        name: 'Alpha',
        kind: 'class',
        line: 1,
        indent: 0,
        children: [{ name: 'alphaChild', kind: 'method', line: 2, indent: 1 }],
    },
    { name: 'Beta', kind: 'class', line: 10, indent: 0 },
];

/** Rows are titled `Line {n}: {name}`, which is how a row is found by symbol. */
const childRow = () => document.querySelector('[title="Line 2: alphaChild"]');
const collapseToggle = () => document.querySelector<HTMLElement>('[aria-label="Collapse"]');

function renderPanel(props: Record<string, unknown> = {}) {
    return render(BranchSymbols, {
        props: { symbols: SYMBOLS, filePath: '/p/alpha.ts', onSymbolClick: () => {}, ...props },
    }) as unknown as { rerender: (props: Record<string, unknown>) => Promise<void> };
}

beforeEach(() => {
    vi.clearAllMocks();
});

afterEach(() => cleanup());

describe('symbol tree collapse state', () => {
    it('is forgotten when another file is shown and restored when it comes back', async () => {
        const { rerender } = renderPanel();
        // Tree view is read from the persisted setting, so the chevron appears asynchronously.
        await waitFor(() => expect(collapseToggle()).toBeTruthy());
        expect(childRow()).toBeTruthy();

        fireEvent.click(collapseToggle()!);
        await waitFor(() => expect(childRow()).toBeNull());

        await rerender({ filePath: '/p/beta.ts' });
        expect(childRow()).toBeTruthy();

        await rerender({ filePath: '/p/alpha.ts' });
        expect(childRow()).toBeNull();
    });

    it('leaves another file with the same tree expanded', async () => {
        const { rerender } = renderPanel();
        await waitFor(() => expect(collapseToggle()).toBeTruthy());

        fireEvent.click(collapseToggle()!);
        await waitFor(() => expect(childRow()).toBeNull());

        await rerender({ filePath: '/p/gamma.ts' });
        expect(childRow()).toBeTruthy();
    });
});
