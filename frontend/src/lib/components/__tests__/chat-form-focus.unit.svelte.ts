import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render, fireEvent, waitFor, cleanup } from '@testing-library/svelte';

import ChatForm from '$lib/components/Chat/ChatForm.svelte';

// ============================================================
// CHAT FORM — FOCUS ENTERING / LEAVING THE CHAT INPUT (Task View auto-maximize)
// ============================================================
// The panel maximizes while the chat input holds focus, and the input is the only thing that
// reports it: every other control of the form — the model bar (provider, model, compaction,
// operating mode) and attach / send / stop — is transparent in both directions, so tapping one
// never moves the task view in or out of full screen. A leave is focus going outside the form
// or nowhere; the @ mention list is not a leave — it renders outside the form and hands focus
// back to the input as soon as a row is picked. (jsdom never dispatches focusout from
// element.blur(), so Escape's blur-first path is not exercised here.)

const baseProps = {
    projectId: 'proj-1',
    taskId: 'task-1',
    onSubmit: vi.fn(),
    onStop: vi.fn(),
};

/** Enough props for the model bar to render both selectors, the donut and the mode toggle. */
const modelBarProps = {
    providers: [
        { name: 'OpenAI', url: 'https://api.openai.com', api_key: '', models: [{ name: 'gpt-a' }] },
        { name: 'Local', url: 'http://localhost:8080', api_key: '', models: [{ name: 'llama' }] },
    ],
    selectedModel: 'gpt-a',
    selectedUrl: 'https://api.openai.com',
    contextUsed: 1200,
    contextLimit: 200000,
    onOperatingModeToggle: vi.fn(),
};

beforeAll(() => {
    Element.prototype.scrollIntoView = vi.fn();
});

afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
});

function renderForm(props = {}) {
    const onInputFocus = vi.fn();
    const onInputBlur = vi.fn();
    const { container } = render(ChatForm, { props: { ...baseProps, ...props, onInputFocus, onInputBlur } });
    const textarea = container.querySelector<HTMLTextAreaElement>('#chat-message-input')!;
    return { container, textarea, onInputFocus, onInputBlur };
}

describe('ChatForm focus notification', () => {
    it('reports focus entering the textarea', () => {
        const { textarea, onInputFocus, onInputBlur } = renderForm();
        fireEvent.focus(textarea);
        expect(onInputFocus).toHaveBeenCalledTimes(1);
        expect(onInputBlur).not.toHaveBeenCalled();
    });

    it('reports a blur with no element on the other side', () => {
        const { textarea, onInputBlur } = renderForm();

        fireEvent.blur(textarea, { relatedTarget: null });

        expect(onInputBlur).toHaveBeenCalledTimes(1);
    });

    it('reports focus that leaves the form', () => {
        const { textarea, onInputBlur } = renderForm();
        const outside = document.createElement('button');
        document.body.appendChild(outside);

        fireEvent.blur(textarea, { relatedTarget: outside });

        expect(onInputBlur).toHaveBeenCalledTimes(1);
    });

    it('stays quiet while focus moves to one of the form’s own controls', () => {
        const { container, textarea, onInputFocus, onInputBlur } = renderForm();
        const attach = container.querySelector<HTMLButtonElement>('.btn-attach')!;

        fireEvent.focus(textarea);
        // The pair a browser sends for one move: out of the input, into the next control.
        fireEvent.blur(textarea, { relatedTarget: attach });
        fireEvent.focusIn(attach, { relatedTarget: textarea });

        expect(onInputFocus).toHaveBeenCalledTimes(1);
        expect(onInputBlur).not.toHaveBeenCalled();
    });

    it('reports nothing for the model bar: provider, model, compaction, operating mode', () => {
        const { container, onInputFocus, onInputBlur } = renderForm(modelBarProps);
        const controls = [
            '.provider-trigger',
            '.model-trigger',
            '.context-usage',
            '.btn-plan-toggle',
        ];
        for (const selector of controls) {
            const control = container.querySelector<HTMLElement>(selector);
            expect(control, selector).not.toBeNull();
            fireEvent.focusIn(control!);
            fireEvent.focusOut(control!, { relatedTarget: null });
        }

        expect(onInputFocus).not.toHaveBeenCalled();
        expect(onInputBlur).not.toHaveBeenCalled();
    });

    it('reports nothing for a control other than the textarea', () => {
        // A streaming turn shows ■ Stop, a draft shows ➤ Send; both share the attach button.
        const streaming = renderForm({ isLoading: true });
        const drafting = renderForm({ value: 'draft' });
        const seen = new Set<string>();

        for (const { container, onInputFocus, onInputBlur } of [streaming, drafting]) {
            for (const selector of ['.btn-attach', '.btn-stop', '.btn-send']) {
                const control = container.querySelector<HTMLButtonElement>(selector);
                if (!control) continue;
                seen.add(selector);
                fireEvent.focusIn(control);
                fireEvent.focusOut(control, { relatedTarget: null });
            }
            expect(onInputFocus).not.toHaveBeenCalled();
            expect(onInputBlur).not.toHaveBeenCalled();
        }

        expect([...seen].sort()).toEqual(['.btn-attach', '.btn-send', '.btn-stop']);
    });

    it('does not report a leave while the @ mention list is open', async () => {
        vi.stubGlobal('fetch', vi.fn(async () => ({
            ok: true,
            json: async () => ({ results: [{ path: 'README.md' }] }),
        })));

        const { container, textarea, onInputBlur } = renderForm();
        textarea.focus();
        fireEvent.input(textarea, { target: { value: '@' } });
        textarea.setSelectionRange(1, 1);

        await waitFor(() => expect(container.querySelector('.mention-dropdown')).not.toBeNull());

        fireEvent.blur(textarea, { relatedTarget: null });

        expect(onInputBlur).not.toHaveBeenCalled();
    });
});
