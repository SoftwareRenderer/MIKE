import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render, fireEvent, cleanup } from '@testing-library/svelte';

import ChatForm from '$lib/components/Chat/ChatForm.svelte';

// ============================================================
// CHAT FORM — FOCUS ENTERING / LEAVING THE FORM (Task View chat input)
// ============================================================
// The panel maximizes while the chat form holds focus, so "holds focus" is the form as a
// whole: a move between its own controls must not report a leave, and neither may the
// @ mention list (rendered outside the form, hands focus back to the textarea on pick).
// A focusout with nothing on the other side — the keyboard went away, focus fell to the
// page — is a leave. (jsdom never dispatches focusout from element.blur(), so Escape's
// blur-first path is not exercised here.)

const baseProps = {
    providers: [],
    projectId: 'proj-1',
    taskId: 'task-1',
    onSubmit: vi.fn(),
    onStop: vi.fn(),
};

beforeAll(() => {
    Element.prototype.scrollIntoView = vi.fn();
});

afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
});

function renderForm() {
    const onInputFocus = vi.fn();
    const onInputBlur = vi.fn();
    const { container } = render(ChatForm, { props: { ...baseProps, onInputFocus, onInputBlur } });
    const textarea = container.querySelector<HTMLTextAreaElement>('#chat-message-input')!;
    const form = container.querySelector('form.chat-form')!;
    return { container, form, textarea, onInputFocus, onInputBlur };
}

describe('ChatForm focus notification', () => {
    it('reports focus entering the textarea', () => {
        const { textarea, onInputFocus, onInputBlur } = renderForm();
        fireEvent.focusIn(textarea);
        expect(onInputFocus).toHaveBeenCalledTimes(1);
        expect(onInputBlur).not.toHaveBeenCalled();
    });

    it('stays quiet while focus moves between the form’s own controls', () => {
        const { container, textarea, onInputFocus, onInputBlur } = renderForm();
        const attach = container.querySelector<HTMLButtonElement>('.btn-attach')!;

        fireEvent.focusIn(textarea);
        // The pair a browser sends for one move: out of one control, into the next.
        fireEvent.focusOut(textarea, { relatedTarget: attach });
        fireEvent.focusIn(attach, { relatedTarget: textarea });

        expect(onInputFocus).toHaveBeenCalledTimes(1);
        expect(onInputBlur).not.toHaveBeenCalled();
    });

    it('reports focus that leaves the form', () => {
        const { container, textarea, onInputBlur } = renderForm();
        const outside = document.createElement('button');
        container.parentElement!.appendChild(outside);

        fireEvent.focusOut(textarea, { relatedTarget: outside });

        expect(onInputBlur).toHaveBeenCalledTimes(1);
    });

    it('reports a blur with no element on the other side', () => {
        const { textarea, onInputBlur } = renderForm();

        fireEvent.focusOut(textarea, { relatedTarget: null });

        expect(onInputBlur).toHaveBeenCalledTimes(1);
    });

    it('reports focus entering through a control other than the textarea', () => {
        const { container, onInputFocus } = renderForm();

        fireEvent.focusIn(container.querySelector<HTMLButtonElement>('.btn-attach')!);

        expect(onInputFocus).toHaveBeenCalledTimes(1);
    });
});
