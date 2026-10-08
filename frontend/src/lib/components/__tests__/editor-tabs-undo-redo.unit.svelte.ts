import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, cleanup, waitFor, fireEvent } from '@testing-library/svelte';
import { tick } from 'svelte';

/**
 * The tab bar can only know the active file's undo state from what that editor reports, so these tests
 * stub `FileEditor` and capture the `onHistoryChange` callback each instance is handed.
 */

interface History {
    canUndo: boolean;
    canRedo: boolean;
    undo: () => void;
    redo: () => void;
}

/** A report without its commands; the stub supplies its own spies for those. */
type Flags = Pick<History, 'canUndo' | 'canRedo'>;

const reporters = new Map<string, (flags: Flags) => void>();
const commands = new Map<string, { undo: ReturnType<typeof vi.fn>; redo: ReturnType<typeof vi.fn> }>();

vi.mock('$lib/components/FileEditor.svelte', () => ({
    default: (_anchor: unknown, props: { filePath: string; onHistoryChange: (history: History) => void }) => {
        const undo = vi.fn();
        const redo = vi.fn();
        commands.set(props.filePath, { undo, redo });
        reporters.set(props.filePath, (flags) => {
            props.onHistoryChange({ ...flags, undo, redo });
        });
    },
}));

vi.mock('$lib/stores', () => ({ appStore: { config: {} } }));

import EditorTabs from '$lib/components/EditorTabs.svelte';
import { t } from '$lib/i18n';

const A = { path: '/p/alpha.ts', targetLine: 0, reveal: 0 };
const B = { path: '/p/beta.ts', targetLine: 0, reveal: 0 };

const undoButton = (c: HTMLElement) =>
    c.querySelector<HTMLButtonElement>(`[aria-label="${t('editor.undo')}"]`);
const redoButton = (c: HTMLElement) =>
    c.querySelector<HTMLButtonElement>(`[aria-label="${t('editor.redo')}"]`);
const undoDisabled = (c: HTMLElement) => undoButton(c)!.disabled;

function renderTabs(props: Record<string, unknown> = {}) {
    const result = render(EditorTabs, {
        props: {
            projectId: 'proj',
            projectPath: '/p',
            openFiles: [A, B],
            activeFilePath: A.path,
            unsavedFiles: new Set<string>(),
            pinnedFiles: new Set<string>(),
            onFocused: () => {},
            onSymbolsUpdate: () => {},
            onHasChanges: () => {},
            onSelectTab: () => {},
            onCloseTab: () => {},
            onPinTab: () => {},
            onSave: () => {},
            scrollTops: {},
            onScrollTop: () => {},
            isMobile: true,
            ...props,
        },
    }) as unknown as { container: HTMLElement; rerender: (props: Record<string, unknown>) => Promise<void> };
    return result;
}

async function openTabs(props: Record<string, unknown> = {}) {
    const rendered = renderTabs(props);
    await waitFor(() => expect(reporters.size).toBe(2));
    return rendered;
}

beforeEach(() => {
    reporters.clear();
    commands.clear();
});

afterEach(() => cleanup());

describe('the tab bar’s undo/redo pair', () => {
    it('gives a wide layout neither button', async () => {
        const { container } = await openTabs({ isMobile: false });
        expect(undoButton(container)).toBeNull();
        expect(redoButton(container)).toBeNull();
    });

    it('dims both directions until an editor reports a stack', async () => {
        const { container } = await openTabs();
        expect(undoDisabled(container)).toBe(true);
        expect(redoButton(container)!.disabled).toBe(true);
    });

    it('lights undo for what the active file reports, and nothing an inactive one reports', async () => {
        const { container } = await openTabs();

        reporters.get(B.path)!({ canUndo: true, canRedo: false });
        await tick();
        expect(undoDisabled(container)).toBe(true);

        reporters.get(A.path)!({ canUndo: true, canRedo: false });
        await tick();
        expect(undoDisabled(container)).toBe(false);
    });

    it('follows the file tabbed into, not the one left behind', async () => {
        const { container, rerender } = await openTabs();
        reporters.get(A.path)!({ canUndo: true, canRedo: false });
        await tick();
        expect(undoDisabled(container)).toBe(false);

        await rerender({ activeFilePath: B.path });
        expect(undoDisabled(container)).toBe(true);

        reporters.get(B.path)!({ canUndo: false, canRedo: true });
        await tick();
        expect(undoButton(container)!.disabled).toBe(true);
        expect(redoButton(container)!.disabled).toBe(false);
    });

    it('runs the command the active file reported', async () => {
        const { container } = await openTabs();
        reporters.get(A.path)!({ canUndo: true, canRedo: true });
        reporters.get(B.path)!({ canUndo: true, canRedo: true });
        await tick();

        await fireEvent.click(undoButton(container)!);
        expect(commands.get(A.path)!.undo).toHaveBeenCalledTimes(1);
        expect(commands.get(B.path)!.undo).not.toHaveBeenCalled();

        await fireEvent.click(redoButton(container)!);
        expect(commands.get(A.path)!.redo).toHaveBeenCalledTimes(1);
        expect(commands.get(B.path)!.redo).not.toHaveBeenCalled();
    });

    it('dims again when the file whose stack was lit closes', async () => {
        const { container, rerender } = await openTabs();
        reporters.get(A.path)!({ canUndo: true, canRedo: true });
        await tick();
        expect(undoDisabled(container)).toBe(false);

        await rerender({ openFiles: [B], activeFilePath: B.path });
        expect(undoDisabled(container)).toBe(true);
        expect(redoButton(container)!.disabled).toBe(true);
    });

    it('sits left of the full-screen button', async () => {
        const { container } = await openTabs({ onToggleFullscreen: () => {} });
        const actions = container.querySelector('.editor-tab-actions')!;
        const buttons = [...actions.querySelectorAll('button')];
        expect(buttons.indexOf(undoButton(container)!)).toBe(0);
        expect(buttons.indexOf(redoButton(container)!)).toBe(1);
        expect(buttons[2]).toBe(
            actions.querySelector(`[aria-label="${t('app.shortcut.toggleFullscreen')}"]`)
        );
    });
});
