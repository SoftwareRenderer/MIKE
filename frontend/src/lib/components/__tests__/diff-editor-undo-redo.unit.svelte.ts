// @vitest-environment jsdom
// The narrow layout's undo/redo pair: both dim until the direction has something behind it, and a tap
// moves the real CodeMirror stack. The wide layout keeps the keyboard and gets neither button.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, cleanup, act, fireEvent } from '@testing-library/svelte';
import type { EditorView } from '@codemirror/view';

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

// No draft, so the stack starts empty and the first keystroke is the test's own.
vi.mock('$lib/storage', () => ({
    getEditorDraft: () => Promise.resolve(null),
    setEditorDraft: () => Promise.resolve(),
    clearEditorDraft: () => Promise.resolve(),
    loadEditorHistory: () => Promise.resolve(null),
    saveEditorHistory: () => Promise.resolve(),
    loadAppSetting: () => Promise.resolve(false),
    saveAppSetting: () => Promise.resolve(),
}));

// No language server here; the editor must reach its document without one.
vi.mock('$lib/utils/lsp-connection-manager', () => ({
    isLSPAvailable: () => false,
    requestLSPChannel: () => ({ release: () => {} }),
}));

import FileEditor from '$lib/components/FileEditor.svelte';
import { t } from '$lib/i18n';

const DISK = 'alpha';

const undoButton = (c: HTMLElement) =>
    c.querySelector<HTMLButtonElement>(`[aria-label="${t('editor.undo')}"]`);
const redoButton = (c: HTMLElement) =>
    c.querySelector<HTMLButtonElement>(`[aria-label="${t('editor.redo')}"]`);
const viewOf = (c: HTMLElement) =>
    (c.querySelector('.cm-editor') as unknown as { __view: EditorView }).__view;

async function open(props: Record<string, unknown> = {}): Promise<HTMLElement> {
    const { container } = render(FileEditor, {
        props: {
            projectId: 'p1',
            projectPath: '/repo',
            filePath: '/repo/a.go',
            targetLine: 0,
            mode: 'diff',
            displayPath: 'a.go',
            onClose: () => {},
            ...props,
        },
    });
    // The two reads (the file, then its baseline), then the frame the editor is built in.
    await act(async () => {
        await vi.advanceTimersByTimeAsync(100);
    });
    expect(container.querySelector('.cm-content')).toBeTruthy();
    return container;
}

/** A keystroke, then the flush that repaints the buttons. */
async function type(container: HTMLElement, text: string) {
    await act(async () => {
        viewOf(container).dispatch({ changes: { from: 0, insert: text }, userEvent: 'input.type' });
    });
}

beforeEach(() => {
    vi.useFakeTimers();
    readFsFile.mockReset().mockResolvedValue({ content: DISK });
});

afterEach(() => {
    cleanup();
    vi.useRealTimers();
});

describe('the diff editor’s undo/redo pair', () => {
    it('gives a wide layout neither button', async () => {
        const container = await open();
        expect(undoButton(container)).toBeNull();
        expect(redoButton(container)).toBeNull();
    });

    it('dims both directions on a document nobody has typed in', async () => {
        const container = await open({ isMobile: true });
        expect(undoButton(container)!.disabled).toBe(true);
        expect(redoButton(container)!.disabled).toBe(true);
    });

    it('lights undo for a change and keeps redo dim', async () => {
        const container = await open({ isMobile: true });
        await type(container, 'x');
        expect(undoButton(container)!.disabled).toBe(false);
        expect(redoButton(container)!.disabled).toBe(true);
    });

    it('takes the change back and lights redo', async () => {
        const container = await open({ isMobile: true });
        await type(container, 'x');
        await fireEvent.click(undoButton(container)!);
        expect(viewOf(container).state.doc.toString()).toBe(DISK);
        expect(undoButton(container)!.disabled).toBe(true);
        expect(redoButton(container)!.disabled).toBe(false);
    });

    it('puts the change down again when redo is tapped', async () => {
        const container = await open({ isMobile: true });
        await type(container, 'x');
        await fireEvent.click(undoButton(container)!);
        await fireEvent.click(redoButton(container)!);
        expect(viewOf(container).state.doc.toString()).toBe(`x${DISK}`);
        expect(undoButton(container)!.disabled).toBe(false);
        expect(redoButton(container)!.disabled).toBe(true);
    });

    it('sits left of the full-screen button', async () => {
        const container = await open({ isMobile: true, onToggleFullscreen: () => {} });
        const actions = container.querySelector('.editor-actions')!;
        const buttons = [...actions.querySelectorAll('button')];
        expect(buttons.indexOf(undoButton(container)!)).toBe(0);
        expect(buttons.indexOf(redoButton(container)!)).toBe(1);
        expect(buttons[2]).toBe(actions.querySelector('.editor-fullscreen-toggle'));
    });
});
