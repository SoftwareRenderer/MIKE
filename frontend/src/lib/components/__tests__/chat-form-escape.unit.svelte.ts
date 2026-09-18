import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from 'vitest';
import { render, fireEvent, waitFor, cleanup } from '@testing-library/svelte';

import ChatForm from '$lib/components/Chat/ChatForm.svelte';

// ============================================================
// CHAT FORM — ESCAPE SEMANTICS (Task View chat input)
// ============================================================
// Escape in the chat input never cancels generation: an in-flight turn is
// stopped only via the ■ Stop button. Escape always blurs the input and
// swallows the event (blur-first), so the Task View's window-level
// "back to board" Escape fires on the NEXT press, not the blurred one.
// With the @ mention dropdown open, Escape closes the dropdown and keeps
// focus in the input.

const baseProps = {
    providers: [],
    projectId: 'proj-1',
    taskId: 'task-1',
    onSubmit: vi.fn(),
    onStop: vi.fn(),
};

beforeAll(() => {
    // jsdom implements neither of these; ChatForm's focus handler / auto-resize call them.
    Element.prototype.scrollIntoView = vi.fn();
});

beforeEach(() => {
    // Polyfill matchMedia for useIsMobile (desktop: never matches)
    vi.stubGlobal('matchMedia', (query: string) => ({
        matches: false,
        media: query,
        onchange: null,
        addEventListener: () => {},
        removeEventListener: () => {},
        addListener: () => {},
        removeListener: () => {},
        dispatchEvent: () => false,
    }));
});

afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
});

function textareaOf(container: HTMLElement): HTMLTextAreaElement {
    const el = container.querySelector<HTMLTextAreaElement>('#chat-message-input');
    expect(el).not.toBeNull();
    return el!;
}

/** Collects Escape keydowns that reach the window (CardDetail's handler). */
function windowEscapes(): { events: KeyboardEvent[]; off: () => void } {
    const events: KeyboardEvent[] = [];
    const handler = (e: KeyboardEvent) => events.push(e);
    window.addEventListener('keydown', handler);
    return { events, off: () => window.removeEventListener('keydown', handler) };
}

describe('ChatForm Escape behavior', () => {
    it('Escape while streaming does NOT stop generation', () => {
        const onStop = vi.fn();
        const { container } = render(ChatForm, { props: { ...baseProps, isLoading: true, onStop } });

        const textarea = textareaOf(container);
        textarea.focus();
        expect(document.activeElement).toBe(textarea);

        fireEvent.keyDown(textarea, { key: 'Escape', bubbles: true, cancelable: true });

        // Generation keeps running — stopping is the Stop button's job.
        expect(onStop).not.toHaveBeenCalled();
        // The input was blurred instead.
        expect(document.activeElement).not.toBe(textarea);
    });

    it('Escape behaves identically when idle: blurs without stopping', () => {
        const onStop = vi.fn();
        const { container } = render(ChatForm, { props: { ...baseProps, isLoading: false, onStop } });

        const textarea = textareaOf(container);
        textarea.focus();

        fireEvent.keyDown(textarea, { key: 'Escape', bubbles: true, cancelable: true });

        expect(onStop).not.toHaveBeenCalled();
        expect(document.activeElement).not.toBe(textarea);
    });

    it('Escape preserves the draft text', () => {
        const { container } = render(ChatForm, { props: { ...baseProps, isLoading: true } });

        const textarea = textareaOf(container);
        textarea.focus();
        fireEvent.input(textarea, { target: { value: 'draft before escape' } });

        fireEvent.keyDown(textarea, { key: 'Escape', bubbles: true, cancelable: true });

        expect(textarea.value).toBe('draft before escape');
    });

    it('the blurred Escape does not reach the window (blur-first), the next one does', () => {
        const { container } = render(ChatForm, { props: { ...baseProps, isLoading: true } });
        const tracker = windowEscapes();

        try {
            const textarea = textareaOf(container);
            textarea.focus();

            // Press 1 — consumed by the chat input (blur), Task View stays open.
            fireEvent.keyDown(textarea, { key: 'Escape', bubbles: true, cancelable: true });
            expect(tracker.events.length).toBe(0);
            expect(document.activeElement).not.toBe(textarea);

            // Press 2 — focus already left the input, so the event rides up to
            // the window, where CardDetail closes the Task View (back to board).
            fireEvent.keyDown(document.body, { key: 'Escape', bubbles: true, cancelable: true });
            expect(tracker.events.length).toBe(1);
        } finally {
            tracker.off();
        }
    });

    it('Escape closes the @ mention dropdown first and keeps focus in the input', async () => {
        vi.stubGlobal('fetch', vi.fn(async () => ({
            ok: true,
            json: async () => ({ results: [{ path: 'README.md' }] }),
        })));

        const onStop = vi.fn();
        const { container } = render(ChatForm, { props: { ...baseProps, isLoading: true, onStop } });
        const tracker = windowEscapes();

        try {
            const textarea = textareaOf(container);
            textarea.focus();
            fireEvent.input(textarea, { target: { value: '@' } });
            textarea.setSelectionRange(1, 1);

            await waitFor(() => expect(container.querySelector('.mention-dropdown')).not.toBeNull());

            fireEvent.keyDown(textarea, { key: 'Escape', bubbles: true, cancelable: true });

            await waitFor(() => expect(container.querySelector('.mention-dropdown')).toBeNull());
            // Focus stays in the input, nothing is blurred, nothing is stopped.
            expect(document.activeElement).toBe(textarea);
            expect(onStop).not.toHaveBeenCalled();
            expect(tracker.events.length).toBe(0);
        } finally {
            tracker.off();
        }
    });

    it('the ■ Stop button still stops generation while streaming', () => {
        const onStop = vi.fn();
        const { container } = render(ChatForm, { props: { ...baseProps, isLoading: true, onStop } });

        const stopBtn = container.querySelector<HTMLButtonElement>('.btn-stop');
        expect(stopBtn).not.toBeNull();

        fireEvent.click(stopBtn!);

        expect(onStop).toHaveBeenCalledTimes(1);
    });
});
