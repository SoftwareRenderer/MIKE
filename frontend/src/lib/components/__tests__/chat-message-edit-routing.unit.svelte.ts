import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';

// Which of the three edits an edited user message becomes: rewrite the row, re-run the thread from it, or
// open a new thread with it. The button's label and the callback it triggers are one decision read twice, so
// both are asserted here — a label that disagrees with its callback is a click the user cannot predict.
// The summarized-context branch sits in front of this decision in ChatMessage.submitEdit, so the routing it
// did not change is pinned here rather than left to reading the branches.

window.matchMedia = vi.fn().mockImplementation((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
}));

vi.mock('$lib/api', () => ({ getTaskMessageFull: vi.fn(), uploadTaskFile: vi.fn() }));

import ChatMessage from '$lib/components/Chat/ChatMessage.svelte';

afterEach(cleanup);

const PENCIL_TITLE = 'Edit / new thread';

function submitButton(container: HTMLElement): HTMLButtonElement {
    const submit = container.querySelector('.btn-edit-submit') as HTMLButtonElement;
    expect(submit).toBeTruthy();
    return submit;
}

async function openEdit(container: HTMLElement) {
    await fireEvent.click(container.querySelector(`button[title="${PENCIL_TITLE}"]`)!);
    return submitButton(container);
}

/** Replace the message's own text with `text` and submit. */
async function edit(container: HTMLElement, text: string) {
    const submit = await openEdit(container);
    await type(container, submit, text);
}

async function type(container: HTMLElement, submit: HTMLButtonElement, text: string) {
    const textarea = container.querySelector('textarea') as HTMLTextAreaElement;
    textarea.value = text;
    await fireEvent.input(textarea);
    await fireEvent.click(submit);
}

/** A started task: the thread has an answer to edit around. A backlog task saves whatever is edited. */
const RUNNING = { taskStatus: 'in_progress' };

function renderUser(props: Record<string, unknown>) {
    return render(ChatMessage, {
        props: { role: 'user', content: 'the original question', messageId: 'm1', ...props },
    });
}

describe('editing a user message', () => {
    it('sends the last message again', async () => {
        const onSend = vi.fn(async () => { });
        const onFork = vi.fn(async () => { });
        const { container } = renderUser({ ...RUNNING, isFirstUserMessage: false, isLastUserMessage: true, onSend, onFork });
        await edit(container, 'asked again, differently');
        expect(onSend).toHaveBeenCalledWith('m1', 'the original question', 'asked again, differently', undefined);
        expect(onFork).not.toHaveBeenCalled();
    });

    it('forks a thread from a message in the middle of the thread', async () => {
        const onSend = vi.fn(async () => { });
        const onFork = vi.fn(async () => { });
        const { container } = renderUser({ ...RUNNING, isFirstUserMessage: false, isLastUserMessage: false, onSend, onFork });
        await edit(container, 'a different branch');
        expect(onFork).toHaveBeenCalledWith('m1', 'the original question', 'a different branch', undefined);
        expect(onSend).not.toHaveBeenCalled();
    });

    it('forks rather than reuses the turn when the first message is also the last', async () => {
        const onSend = vi.fn(async () => { });
        const onFork = vi.fn(async () => { });
        const { container } = renderUser({ ...RUNNING, isFirstUserMessage: true, isLastUserMessage: true, onSend, onFork });
        await edit(container, 'a new thread from the opening question');
        expect(onFork).toHaveBeenCalled();
        expect(onSend).not.toHaveBeenCalled();
    });

    it('sends an edit that carries only an attachment', async () => {
        const onSend = vi.fn(async () => { });
        const onFork = vi.fn(async () => { });
        const { container } = renderUser({ ...RUNNING, isFirstUserMessage: false, isLastUserMessage: true, projectId: 'p1', taskId: 't1', onSend, onFork });
        const submit = await openEdit(container);
        const form = container.querySelector('.edit-form') as HTMLElement;
        await fireEvent.drop(form, {
            dataTransfer: { files: [new File(['img'], 'shot.png', { type: 'image/png' })], types: ['Files'], getData: () => '' },
        });
        // The image is read before it becomes an attachment, so the chip is the moment it landed.
        await waitFor(() => expect(container.querySelector('.edit-attachment-chip')).toBeTruthy());
        await type(container, submit, '');
        // The form allows an attachment with no text, so the routing must not contradict it.
        expect(onSend).toHaveBeenCalledTimes(1);
        const [id, original, text, atts] = onSend.mock.calls[0] as unknown as [string, string, string, unknown[]];
        expect(id).toBe('m1');
        expect(original).toBe('the original question');
        expect(text).toBe('');
        expect(atts).toHaveLength(1);
        expect(onFork).not.toHaveBeenCalled();
    });

    it('saves whatever is edited while the task is still in the backlog', async () => {
        const onSend = vi.fn(async () => { });
        const onFork = vi.fn(async () => { });
        const { container } = renderUser({ taskStatus: 'backlog', isFirstUserMessage: false, isLastUserMessage: false, onSend, onFork });
        const submit = await openEdit(container);
        expect(submit.title).toBe('Save');
        await type(container, submit, 'worded differently before anything ran');
        // The backlog branch of the send handler persists the text and starts nothing.
        expect(onSend).toHaveBeenCalledWith('m1', 'the original question', 'worded differently before anything ran', undefined);
        expect(onFork).not.toHaveBeenCalled();
    });
});

describe('what the submit button says', () => {    it('says Send, with the send icon, for the message that can be answered again', async () => {
        const { container } = renderUser({ ...RUNNING, isFirstUserMessage: false, isLastUserMessage: true, onSend: vi.fn(async () => { }), onFork: vi.fn(async () => { }) });
        const submit = await openEdit(container);
        expect(submit.title).toBe('Send');
        expect(submit.textContent).toContain('Send');
        expect(submit.querySelector('svg polygon')).toBeTruthy();
    });

    it('says New Thread, with the fork icon, when the first message is also the last', async () => {
        const { container } = renderUser({ ...RUNNING, isFirstUserMessage: true, isLastUserMessage: true, onSend: vi.fn(async () => { }), onFork: vi.fn(async () => { }) });
        const submit = await openEdit(container);
        expect(submit.title).toBe('New Thread');
        expect(submit.textContent).toContain('New Thread');
        expect(submit.querySelector('svg circle')).toBeTruthy();
    });

    it('says New Thread for a middle message, where a rewrite would erase the turns after it', async () => {
        const { container } = renderUser({ ...RUNNING, isFirstUserMessage: false, isLastUserMessage: false, onSend: vi.fn(async () => { }), onFork: vi.fn(async () => { }) });
        const submit = await openEdit(container);
        expect(submit.title).toBe('New Thread');
    });
});

describe('editing while a turn is in flight', () => {
    it('offers no way to edit a message', () => {
        const { container } = renderUser({ ...RUNNING, turnInFlight: true, isFirstUserMessage: false, isLastUserMessage: true, onSend: vi.fn(async () => { }), onFork: vi.fn(async () => { }) });
        expect(container.querySelector(`button[title="${PENCIL_TITLE}"]`)).toBeNull();
    });

    it('offers it again once the turn has ended', () => {
        const { container } = renderUser({ ...RUNNING, turnInFlight: false, isFirstUserMessage: false, isLastUserMessage: true, onSend: vi.fn(async () => { }), onFork: vi.fn(async () => { }) });
        expect(container.querySelector(`button[title="${PENCIL_TITLE}"]`)).toBeTruthy();
    });
});
