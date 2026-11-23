import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, cleanup, act, waitFor } from '@testing-library/svelte';

/**
 * The caret a created file asked for: the asked-for tab takes it once its view exists, no other tab
 * spends the ask, and an ask is spent once — a tab becoming active again is not a second request.
 */

const readFsFile = vi.fn();

vi.mock('$lib/api', async (importOriginal) => {
    const real = await importOriginal<typeof import('$lib/api')>();
    return {
        ...real,
        readFsFile: (params: { path: string }) => readFsFile(params),
        writeFsFile: () => Promise.resolve(),
    };
});

vi.mock('$lib/stores', () => ({
    appStore: { config: {} },
    dialogStore: { showToast: () => {} },
}));

vi.mock('$lib/storage', () => ({
    getEditorDraft: () => Promise.resolve(null),
    setEditorDraft: () => Promise.resolve(),
    clearEditorDraft: () => Promise.resolve(),
    loadEditorHistory: () => Promise.resolve(null),
    saveEditorHistory: () => Promise.resolve(),
    loadAppSetting: () => Promise.resolve(false),
    saveAppSetting: () => Promise.resolve(),
}));

vi.mock('$lib/utils/lsp-connection-manager', () => ({
    isLSPAvailable: () => false,
    requestLSPChannel: () => ({ release: () => {} }),
}));

import EditorTabs from '$lib/components/EditorTabs.svelte';

const CREATED = '/p/notes.md';
const OTHER = '/p/other.md';

const createdTab = { path: CREATED, targetLine: 0, reveal: 0 };
const otherTab = { path: OTHER, targetLine: 0, reveal: 0 };

function renderTabs(props: Record<string, unknown> = {}) {
    return render(EditorTabs, {
        props: {
            projectId: 'proj',
            projectPath: '/p',
            openFiles: [createdTab, otherTab],
            activeFilePath: CREATED,
            unsavedFiles: new Set<string>(),
            pinnedFiles: new Set<string>([CREATED, OTHER]),
            onFocused: () => {},
            onSymbolsUpdate: () => {},
            onHasChanges: () => {},
            onSelectTab: () => {},
            onCloseTab: () => {},
            onPinTab: () => {},
            scrollTops: {},
            onScrollTop: () => {},
            ...props,
        },
    }) as unknown as {
        container: HTMLElement;
        rerender: (props: Record<string, unknown>) => Promise<void>;
    };
}

/** The buffer of one tab, in the order the tabs are drawn. */
const contentOf = (container: HTMLElement, path: string) =>
    container.querySelectorAll<HTMLElement>('.cm-content')[
        [CREATED, OTHER].indexOf(path)
    ];

/** Take the caret away the way a click outside the editor does. */
async function loseFocus() {
    await act(async () => {
        document.body.appendChild(document.createElement('input')).focus();
    });
}

beforeEach(() => {
    readFsFile.mockReset().mockImplementation(({ path }: { path: string }) =>
        Promise.resolve({ content: path === CREATED ? '' : 'other bytes\n' }));
});

afterEach(() => cleanup());

describe('the caret a created file asked for', () => {
    it('lands in the asked-for tab once its view exists', async () => {
        const { container } = renderTabs({ focusRequest: { path: CREATED, nonce: 1 } });

        await waitFor(() => expect(contentOf(container, CREATED)).toBeTruthy());
        await waitFor(() => expect(document.activeElement).toBe(contentOf(container, CREATED)));
    });

    it('is not taken by the tab the ask did not name', async () => {
        const { container } = renderTabs({
            activeFilePath: OTHER,
            focusRequest: { path: CREATED, nonce: 1 },
        });

        await waitFor(() => expect(contentOf(container, OTHER)).toBeTruthy());
        await new Promise(r => setTimeout(r, 50));

        expect(document.activeElement).not.toBe(contentOf(container, OTHER));
        expect(document.activeElement).not.toBe(contentOf(container, CREATED));
    });

    it('is spent once, so a tab becoming active again does not steal the caret back', async () => {
        const { container, rerender } = renderTabs({ focusRequest: { path: CREATED, nonce: 1 } });
        await waitFor(() => expect(document.activeElement).toBe(contentOf(container, CREATED)));

        await loseFocus();
        await rerender({ activeFilePath: OTHER });
        await rerender({ activeFilePath: CREATED });
        await new Promise(r => setTimeout(r, 50));

        expect(document.activeElement).not.toBe(contentOf(container, CREATED));
    });

    it('is not taken at all when nothing asked for it', async () => {
        const { container } = renderTabs();

        await waitFor(() => expect(contentOf(container, CREATED)).toBeTruthy());
        await new Promise(r => setTimeout(r, 50));

        expect(document.activeElement).not.toBe(contentOf(container, CREATED));
        expect(document.activeElement).not.toBe(contentOf(container, OTHER));
    });
});
