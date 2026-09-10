import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, cleanup, waitFor } from '@testing-library/svelte';
import { tick } from 'svelte';

/**
 * These tests stub `FileEditor` and capture the `onSymbolsUpdate` callback each instance is
 * handed — the only channel an editor has to report its outline.
 */

interface Sym {
    name: string;
    kind: string;
    line: number;
    endLine: number;
    indent: number;
}

const publishers = new Map<string, (symbols: Sym[]) => void>();

vi.mock('$lib/components/FileEditor.svelte', () => ({
    default: (_anchor: unknown, props: { filePath: string; onSymbolsUpdate: (symbols: Sym[]) => void }) => {
        publishers.set(props.filePath, props.onSymbolsUpdate);
    },
}));

vi.mock('$lib/stores', () => ({ appStore: { config: {} } }));

import EditorTabs from '$lib/components/EditorTabs.svelte';

const ALPHA: Sym[] = [{ name: 'alpha', kind: 'function', line: 3, endLine: 9, indent: 0 }];
const BETA: Sym[] = [{ name: 'beta', kind: 'function', line: 11, endLine: 20, indent: 0 }];
const LATE_BETA: Sym[] = [{ name: 'beta', kind: 'function', line: 99, endLine: 120, indent: 0 }];

const A = { path: '/p/alpha.ts', targetLine: 0, reveal: 0 };
const B = { path: '/p/beta.ts', targetLine: 0, reveal: 0 };
/** Reports nothing, the way a file whose extraction has not finished does. */
const C = { path: '/p/quiet.ts', targetLine: 0, reveal: 0 };

let published: Sym[][] = [];

function renderTabs(props: Record<string, unknown> = {}) {
    return render(EditorTabs, {
        props: {
            projectId: 'proj',
            projectPath: '/p',
            openFiles: [A, B, C],
            activeFilePath: A.path,
            unsavedFiles: new Set<string>(),
            pinnedFiles: new Set<string>(),
            onFocused: () => {},
            onSymbolsUpdate: (symbols: Sym[]) => published.push(symbols),
            onHasChanges: () => {},
            onSelectTab: () => {},
            onCloseTab: () => {},
            onPinTab: () => {},
            scrollTops: {},
            onScrollTop: () => {},
            ...props,
        },
    }) as unknown as { rerender: (props: Record<string, unknown>) => Promise<void> };
}

beforeEach(() => {
    publishers.clear();
    published = [];
});

afterEach(() => cleanup());

describe('outline panel follows the active tab', () => {
    it('keeps an inactive editor outline out of the panel', async () => {
        renderTabs();
        await waitFor(() => expect(publishers.size).toBe(3));

        publishers.get(A.path)!(ALPHA);
        await waitFor(() => expect(published.at(-1)).toBe(ALPHA));

        publishers.get(B.path)!(BETA);
        await tick();
        expect(published).not.toContain(BETA);
    });

    it('shows the outline of the tab clicked into, not of the one left behind', async () => {
        const { rerender } = renderTabs();
        await waitFor(() => expect(publishers.size).toBe(3));
        publishers.get(A.path)!(ALPHA);
        publishers.get(B.path)!(BETA);
        await waitFor(() => expect(published.at(-1)).toBe(ALPHA));

        await rerender({ activeFilePath: B.path });
        expect(published.at(-1)).toBe(BETA);

        await rerender({ activeFilePath: A.path });
        expect(published.at(-1)).toBe(ALPHA);
    });

    it('publishes nothing rather than the previous file outline for a file with no outline yet', async () => {
        const { rerender } = renderTabs();
        await waitFor(() => expect(publishers.size).toBe(3));
        publishers.get(A.path)!(ALPHA);
        await waitFor(() => expect(published.at(-1)).toBe(ALPHA));

        await rerender({ activeFilePath: C.path });
        const last = published.at(-1)!;
        expect(last).toHaveLength(0);
        expect(last).not.toBe(ALPHA);

        publishers.get(C.path)!(BETA);
        await waitFor(() => expect(published.at(-1)).toBe(BETA));
    });

    it('stops publishing the outline of a closed tab', async () => {
        const { rerender } = renderTabs();
        await waitFor(() => expect(publishers.size).toBe(3));
        publishers.get(A.path)!(ALPHA);
        publishers.get(B.path)!(BETA);
        await waitFor(() => expect(published.at(-1)).toBe(ALPHA));

        await rerender({ openFiles: [A, C] });
        publishers.get(B.path)!(LATE_BETA);
        await tick();
        expect(published).not.toContain(LATE_BETA);

        // Reopening a path must not publish the entry pruned when its tab closed.
        await rerender({ openFiles: [A, B, C] });
        expect(published.at(-1)).toBe(ALPHA);
    });
});
