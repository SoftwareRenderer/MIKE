import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup, fireEvent } from '@testing-library/svelte';

// Which of the two edits an edited user message becomes: a forked thread or a sent follow-up. The
// summarized-context branch sits in front of this decision in ChatMessage.submitEdit, so the routing
// it did not change is pinned here rather than left to reading the branches.

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

const EDIT_TITLE = 'Edit / new thread';

async function edit(container: HTMLElement, text: string) {
    await fireEvent.click(container.querySelector(`button[title="${EDIT_TITLE}"]`)!);
    const submit = container.querySelector('.btn-edit-submit') as HTMLButtonElement;
    expect(submit).toBeTruthy();
    // The form starts from the message's own text, so typing means replacing it.
    const textarea = container.querySelector('textarea') as HTMLTextAreaElement;
    textarea.value = text;
    await fireEvent.input(textarea);
    await fireEvent.click(submit);
}

function renderUser(props: Record<string, unknown>) {
    return render(ChatMessage, {
        props: { role: 'user', content: 'the original question', messageId: 'm1', ...props },
    });
}

describe('editing a user message', () => {
    it('sends the last message again', async () => {
        const onSend = vi.fn(async () => {});
        const onFork = vi.fn(async () => {});
        const { container } = renderUser({ isFirstUserMessage: false, isLastUserMessage: true, onSend, onFork });
        await edit(container, 'asked again, differently');
        expect(onSend).toHaveBeenCalledWith('m1', 'the original question', 'asked again, differently', undefined);
        expect(onFork).not.toHaveBeenCalled();
    });

    it('forks a thread from a message in the middle of the thread', async () => {
        const onSend = vi.fn(async () => {});
        const onFork = vi.fn(async () => {});
        const { container } = renderUser({ isFirstUserMessage: false, isLastUserMessage: false, onSend, onFork });
        await edit(container, 'a different branch');
        expect(onFork).toHaveBeenCalledWith('m1', 'the original question', 'a different branch', undefined);
        expect(onSend).not.toHaveBeenCalled();
    });

    it('forks rather than reuses the turn when the first message is also the last', async () => {
        const onSend = vi.fn(async () => {});
        const onFork = vi.fn(async () => {});
        const { container } = renderUser({ isFirstUserMessage: true, isLastUserMessage: true, onSend, onFork });
        await edit(container, 'a new thread from the opening question');
        expect(onFork).toHaveBeenCalled();
        expect(onSend).not.toHaveBeenCalled();
    });
});
