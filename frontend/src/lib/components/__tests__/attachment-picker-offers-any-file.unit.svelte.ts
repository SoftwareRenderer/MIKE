// @vitest-environment jsdom
// The attach button of every surface that takes a file, driven through the real components. What was
// broken here is not reachable from the composable's own tests: the handler classified any file, while
// the picker it was wired to offered images only, so the surface that could take a bundle could never
// be asked for one. The create-task modal is in this file as the counterpart — its picker is meant to
// stay filtered, because it has no task environment to write a file into.
import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import CreateTaskModal from '$lib/components/CreateTaskModal.svelte';
import ChatForm from '$lib/components/Chat/ChatForm.svelte';
import EditForm from '$lib/components/Chat/EditForm.svelte';
import ChatMessage from '$lib/components/Chat/ChatMessage.svelte';
import { appStore, dialogStore } from '$lib/stores';
import { uploadTaskFile } from '$lib/api';

vi.mock('svelte/transition', () => ({ slide: vi.fn(), fade: vi.fn() }));
vi.mock('$lib/api', async (importOriginal) => ({
    ...(await importOriginal<typeof import('$lib/api')>()),
    uploadTaskFile: vi.fn(async () => '/home/sandbox/p1/.mike-uploads/bundle.zip'),
}));

const UPLOADED_PATH = '/home/sandbox/p1/.mike-uploads/bundle.zip';
const archive = () => new File([new Uint8Array([0x50, 0x4b, 0x03, 0x04])], 'bundle.zip', { type: 'application/zip' });

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
    vi.mocked(uploadTaskFile).mockClear();
    appStore.config = null;
    dialogStore.toast = null;
    stubMatchMedia(false);
});

/** jsdom has no file picker: the selection is installed on the input, then the change handler runs. */
function pickInto(input: HTMLInputElement, files: File[]) {
    Object.defineProperty(input, 'files', { value: files, configurable: true });
    fireEvent.change(input);
}

function fileInputIn(container: Element | Document): HTMLInputElement {
    const input = container.querySelector('input[type=file]');
    if (!input) throw new Error('the surface renders no file input');
    return input as HTMLInputElement;
}

describe('the chat input', () => {
    it('names the control for a file and uploads what the picker returns', async () => {
        const { container } = render(ChatForm, { props: { projectId: 'p1', taskId: 't1' } });

        const button = await waitFor(() => {
            const b = container.querySelector('button.btn-attach');
            if (b) return b;
            throw new Error('the chat input renders no attach control');
        });
        expect(button.getAttribute('aria-label')).toBe('Attach file');

        const input = fileInputIn(container);
        expect(input.hasAttribute('accept')).toBe(false);

        const file = archive();
        pickInto(input, [file]);

        await waitFor(() => expect(uploadTaskFile).toHaveBeenCalledWith('p1', 't1', file));
        await waitFor(() =>
            expect((container.querySelector('textarea') as HTMLTextAreaElement).value).toContain(UPLOADED_PATH),
        );
    });
});

describe('the message edit form', () => {
    it('names the control for a file and offers every type', async () => {
        const { container } = render(EditForm, {
            props: { content: 'the message', messageId: 'm1', projectId: 'p1', taskId: 't1' },
        });

        const button = await waitFor(() => {
            const b = container.querySelector('button.btn-edit-attach');
            if (b) return b;
            throw new Error('the edit form renders no attach control');
        });
        expect(button.getAttribute('title')).toBe('Attach file');

        const input = fileInputIn(container);
        expect(input.hasAttribute('accept')).toBe(false);

        const file = archive();
        pickInto(input, [file]);

        await waitFor(() => expect(uploadTaskFile).toHaveBeenCalledWith('p1', 't1', file));
    });
});

describe('the mobile message editor', () => {
    it('offers every type to the message being edited', async () => {
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
        expect(dialog.querySelector('button.edit-attach-btn')?.getAttribute('aria-label')).toBe('Attach file');

        const input = fileInputIn(dialog);
        expect(input.hasAttribute('accept')).toBe(false);

        const file = archive();
        pickInto(input, [file]);

        await waitFor(() => expect(uploadTaskFile).toHaveBeenCalledWith('p1', 't1', file));
    });
});

describe('the task description', () => {
    it('offers images only, and says a file has to wait for the task', async () => {
        const { container } = render(CreateTaskModal, { props: { projectId: 'p1', onClose: () => {} } });

        const input = await waitFor(() => fileInputIn(container));
        expect(input.getAttribute('accept')).toBe('image/png,image/jpeg,image/gif,image/webp');

        pickInto(input, [archive()]);

        await waitFor(() => expect(uploadTaskFile).not.toHaveBeenCalled());
        await waitFor(() => expect(dialogStore.toast?.message).toContain('needs a running task'));
    });
});
