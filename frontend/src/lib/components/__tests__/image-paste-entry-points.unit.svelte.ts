// @vitest-environment jsdom
// The four surfaces that take a paste, driven through the real components: a component that lost its
// wiring still passes the composable's own tests. `fireEvent.paste` cannot assign `clipboardData` in
// jsdom, so the event is built by hand.
import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render, cleanup, waitFor } from '@testing-library/svelte';
import CreateTaskModal from '$lib/components/CreateTaskModal.svelte';
import ChatForm from '$lib/components/Chat/ChatForm.svelte';
import EditForm from '$lib/components/Chat/EditForm.svelte';
import ChatMessage from '$lib/components/Chat/ChatMessage.svelte';
import { appStore } from '$lib/stores';

vi.mock('svelte/transition', () => ({ slide: vi.fn(), fade: vi.fn() }));

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
    appStore.config = null;
    stubMatchMedia(false);
});

const png = (name = 'shot.png') => new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], name, { type: 'image/png' });

/**
 * `DraftAttachment.data` is bare base64, so a preview that renders it straight into `src` is an
 * empty box — the chip and its remove button still render, which is why counting them is not enough.
 */
function expectPreviewImage(scope: Element | Document, chip: string) {
    const src = scope.querySelector(`${chip} img`)?.getAttribute('src') ?? '';
    expect(src.startsWith('data:image/png;base64,')).toBe(true);
}

function pasteInto(el: Element, files: File[], text = '') {
    const event = new Event('paste', { bubbles: true, cancelable: true }) as ClipboardEvent;
    Object.defineProperty(event, 'clipboardData', {
        value: { files, getData: (format: string) => (format === 'text/plain' || format === 'text' ? text : '') },
    });
    el.dispatchEvent(event);
    return event;
}

describe('the task description', () => {
    it('attaches a pasted image and shows it', async () => {
        const { container } = render(CreateTaskModal, { props: { projectId: 'p1', onClose: () => {} } });
        const textarea = await waitFor(() => {
            const t = container.querySelector('#task-description');
            if (t) return t;
            throw new Error('description not rendered');
        });

        pasteInto(textarea, [png()]);

        await waitFor(() => expect(container.querySelectorAll('.task-attachment-chip')).toHaveLength(1));
        expectPreviewImage(container, '.task-attachment-chip');
    });

    it('still forwards a text-bearing paste to a granted createTask.events.paste extension', async () => {
        const { container } = render(CreateTaskModal, { props: { projectId: 'p1', onClose: () => {} } });
        const textarea = await waitFor(() => {
            const t = container.querySelector<HTMLTextAreaElement>('#task-description');
            if (t) return t;
            throw new Error('description not rendered');
        });

        // The grant's own listener, attached the way onPasteGrant attaches it — after Svelte's.
        const seen: string[] = [];
        textarea.addEventListener('paste', (e) => {
            seen.push((e as ClipboardEvent).clipboardData?.getData('text') ?? '');
            e.preventDefault();
        });

        pasteInto(textarea, [png()], 'https://github.com/org/repo/issues/7');

        await waitFor(() => expect(seen).toEqual(['https://github.com/org/repo/issues/7']));
        await waitFor(() => expect(container.querySelectorAll('.task-attachment-chip')).toHaveLength(1));
    });
});

describe('the chat input', () => {
    it('attaches a pasted image and shows it', async () => {
        const { container } = render(ChatForm, { props: { projectId: 'p1', taskId: 't1' } });
        const textarea = await waitFor(() => {
            const t = container.querySelector('textarea');
            if (t) return t;
            throw new Error('chat input not rendered');
        });

        pasteInto(textarea, [png()]);

        await waitFor(() => expect(container.querySelectorAll('.attachment-thumb')).toHaveLength(1));
        expectPreviewImage(container, '.attachment-thumb');
    });

    it('rings the field while a file is dragged over it', async () => {
        const { container } = render(ChatForm, { props: { projectId: 'p1', taskId: 't1' } });
        const inner = await waitFor(() => {
            const el = container.querySelector('.chat-form-inner');
            if (el) return el;
            throw new Error('chat form not rendered');
        });

        const enter = new Event('dragenter', { bubbles: true, cancelable: true }) as DragEvent;
        Object.defineProperty(enter, 'dataTransfer', { value: { types: ['Files'], files: [] } });
        inner.dispatchEvent(enter);

        await waitFor(() => expect(inner.classList.contains('is-drop-target')).toBe(true));
    });
});

describe('the message edit form', () => {
    it('attaches a pasted image and shows it', async () => {
        const { container } = render(EditForm, {
            props: { content: 'the message', messageId: 'm1', projectId: 'p1', taskId: 't1' },
        });
        const textarea = await waitFor(() => {
            const t = container.querySelector('textarea');
            if (t) return t;
            throw new Error('edit form not rendered');
        });

        pasteInto(textarea, [png()]);

        await waitFor(() => expect(container.querySelectorAll('.edit-attachment-chip')).toHaveLength(1));
        expectPreviewImage(container, '.edit-attachment-chip');
    });
});

describe('the mobile message editor', () => {
    it('attaches a pasted image to the message being edited and shows it', async () => {
        stubMatchMedia(true);
        const { container } = render(ChatMessage, {
            props: { role: 'user', content: 'the message', messageId: 'm1', projectId: 'p1', taskId: 't1' },
        });

        const fork = await waitFor(() => {
            const b = container.querySelector<HTMLButtonElement>('button[aria-label="Start new thread from this message"]');
            if (b) return b;
            throw new Error('the message offers no edit action');
        });
        fork.click();

        const textarea = await waitFor(() => {
            const t = document.querySelector<HTMLTextAreaElement>('dialog.edit-dialog textarea');
            if (t) return t;
            throw new Error('the mobile editor did not open');
        });

        pasteInto(textarea, [png()]);

        await waitFor(() =>
            expect(document.querySelectorAll('dialog.edit-dialog .edit-attachment-chip')).toHaveLength(1),
        );
        expectPreviewImage(document, 'dialog.edit-dialog .edit-attachment-chip');
    });
});
