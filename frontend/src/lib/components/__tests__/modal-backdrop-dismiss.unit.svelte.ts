// @vitest-environment jsdom
// The backdrop of a modal answers nothing. A click beside the dialog used to close it, which threw away
// the edited commit message and aborted the preview stream still filling it, so the rule is pinned where
// it lives — `BaseModal`'s overlay — through the two shells dialogs draw, and again on the one dialog
// that builds its own `<dialog>` element: the mobile message editor.
import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import ChatMessage from '$lib/components/Chat/ChatMessage.svelte';
import CommitDialog from '$lib/components/Chat/CommitDialog.svelte';
import ConfirmationDialog from '$lib/components/ConfirmationDialog.svelte';
import { dialogStore } from '$lib/stores';

vi.mock('svelte/transition', () => ({ slide: vi.fn(), fade: vi.fn() }));
vi.mock('$lib/api/tasks', async (importOriginal) => ({
    ...(await importOriginal<typeof import('$lib/api/tasks')>()),
    streamCommitPreview: (...args: unknown[]) => api.streamCommitPreview(...args),
}));

const api = vi.hoisted(() => ({ streamCommitPreview: vi.fn() }));

const DRAFT = 'Fix the gate';

// Svelte 5 effects flush on the microtask queue.
async function flush() {
    for (let i = 0; i < 6; i++) await Promise.resolve();
}

/** The dimmed backdrop of the one modal on screen. */
function overlayOf(scope: HTMLElement): HTMLElement {
    const overlay = scope.querySelector('.overlay');
    if (!overlay) throw new Error('no modal overlay on screen');
    return overlay as HTMLElement;
}

/** A footer button by its own words — `[role="dialog"]` and `.modal-footer` are never unique here. */
function footerButton(container: HTMLElement, label: string): HTMLElement {
    const found = Array.from(container.querySelectorAll('.modal-footer button'))
        .find((b) => (b.textContent ?? '').trim() === label);
    if (!found) throw new Error(`no footer button labelled ${label}`);
    return found as HTMLElement;
}

/** Whether the caller has been answered yet: a dialog that closed on its own settles this. */
function watchAnswer<T>(answered: Promise<T>) {
    const state: { settled: 'pending' | 'answered' } = { settled: 'pending' };
    void answered.then(() => { state.settled = 'answered'; });
    return state;
}

function commitTextarea(container: HTMLElement): HTMLTextAreaElement {
    const textarea = container.querySelector<HTMLTextAreaElement>('.commit-textarea');
    if (!textarea) throw new Error('the commit dialog renders no message field');
    return textarea;
}

function stubMatchMedia(mobile: boolean) {
    vi.stubGlobal('matchMedia', (query: string) => ({
        matches: mobile,
        media: query,
        onchange: null,
        addEventListener: () => {},
        removeEventListener: () => {},
        addListener: () => {},
        removeListener: () => {},
        dispatchEvent: () => false,
    }));
}

beforeAll(() => stubMatchMedia(false));
afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.clearAllMocks();
    dialogStore.commitDialogSlot.clear();
    dialogStore.confirmationSlot.clear();
    stubMatchMedia(false);
});

describe('a click on a modal backdrop', () => {
    it('leaves the commit dialog open with its draft and its caller still waiting', async () => {
        const { container } = render(CommitDialog);
        const answered = dialogStore.showCommitDialog({ message: DRAFT });
        const waiting = watchAnswer(answered);
        await flush();

        expect(commitTextarea(container).value).toBe(DRAFT);

        await fireEvent.click(overlayOf(container));
        await flush();

        expect(container.contains(commitTextarea(container))).toBe(true);
        expect(commitTextarea(container).value).toBe(DRAFT);
        expect(dialogStore.commitDialogSlot.pending).not.toBeNull();
        expect(waiting.settled).toBe('pending');

        // Dismissal still belongs to the footer.
        await fireEvent.click(footerButton(container, 'Cancel'));
        await flush();
        expect(dialogStore.commitDialogSlot.pending).toBeNull();
        await expect(answered).resolves.toBeNull();
    });

    it('does not abort the commit message still streaming', async () => {
        let release: () => void = () => {};
        api.streamCommitPreview.mockImplementation(() =>
            (async function* () {
                yield { event: 'metadata', authorName: 'Dev', authorEmail: 'dev@example.com', mergeTarget: 'main' };
                yield { event: 'token', content: DRAFT };
                await new Promise<void>((resolve) => { release = resolve; });
                yield { event: 'token', content: ' before merge' };
                yield { event: 'done', content: `${DRAFT} before merge` };
            })(),
        );

        const { container } = render(CommitDialog);
        dialogStore.showCommitDialog({ message: '', projectId: 'p1', taskId: 't1' });

        await waitFor(() => expect(commitTextarea(container).value).toBe(DRAFT));
        expect(container.querySelector('.commit-progress')).not.toBeNull();

        await fireEvent.click(overlayOf(container));
        await flush();

        const signal = api.streamCommitPreview.mock.calls[0]?.[2] as AbortSignal | undefined;
        expect(signal?.aborted).toBe(false);
        expect(container.querySelector('.commit-progress')).not.toBeNull();
        expect(dialogStore.commitDialogSlot.pending).not.toBeNull();

        // The stream the click did not stop keeps going.
        release();
        await waitFor(() => expect(commitTextarea(container).value).toBe(`${DRAFT} before merge`));
        expect(container.querySelector('.commit-progress')).toBeNull();
    });

    it('leaves the confirmation dialog open and unanswered', async () => {
        const { container } = render(ConfirmationDialog);
        const answered = dialogStore.showConfirm({ title: 'Drop it', message: 'Three cards go' });
        const waiting = watchAnswer(answered);
        await flush();

        await fireEvent.click(overlayOf(container));
        await flush();

        expect(container.textContent).toContain('Drop it');
        expect(dialogStore.confirmationSlot.pending).not.toBeNull();
        expect(waiting.settled).toBe('pending');

        await fireEvent.click(container.querySelector('.modal-footer .btn-cancel') as HTMLElement);
        await flush();
        expect(dialogStore.confirmationSlot.pending).toBeNull();
        await expect(answered).resolves.toBe(false);
    });
});

describe('the mobile message editor', () => {
    it('keeps the draft when the click lands on the dialog backdrop', async () => {
        stubMatchMedia(true);
        const { container } = render(ChatMessage, {
            props: { role: 'user', content: 'the message', messageId: 'm1', projectId: 'p1', taskId: 't1' },
        });

        const edit = await waitFor(() => {
            const b = container.querySelector<HTMLButtonElement>('button[aria-label="Start new thread from this message"]');
            if (b) return b;
            throw new Error('the message offers no edit action');
        });
        edit.click();

        const dialog = await waitFor(() => {
            const d = document.querySelector('dialog.edit-dialog');
            if (d) return d;
            throw new Error('the mobile editor did not open');
        });
        const textarea = dialog.querySelector('textarea') as HTMLTextAreaElement;
        textarea.value = 'typed over the backdrop';
        fireEvent.input(textarea);
        await flush();

        // The click target is the `<dialog>` itself, which is what its backdrop paints.
        await fireEvent.click(dialog);
        await flush();

        expect(document.contains(dialog)).toBe(true);
        expect((dialog as HTMLDialogElement).open).toBe(true);
        expect((dialog.querySelector('textarea') as HTMLTextAreaElement).value).toBe('typed over the backdrop');
    });
});
