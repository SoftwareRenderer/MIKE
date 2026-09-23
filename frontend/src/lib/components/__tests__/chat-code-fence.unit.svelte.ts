import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup, fireEvent } from '@testing-library/svelte';

// These components must give a fenced code block a copy control, and give it to nothing else.

// jsdom has no matchMedia and ChatMessage reads the viewport through useIsMobile.
window.matchMedia = vi.fn().mockImplementation((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
}));

// Reached through the API layer; none of these cases makes a request.
vi.mock('$lib/api', () => ({ getTaskMessageFull: vi.fn() }));

import ChatMessage from '$lib/components/Chat/ChatMessage.svelte';
import { htmlHazards } from '$lib/utils/__tests__/html-hazards';

afterEach(() => {
    cleanup();
    vi.useRealTimers();
    delete (navigator as { clipboard?: unknown }).clipboard;
});

/** `accepted: false` is a refused write; jsdom implements no `execCommand` fallback either. */
function stubClipboard(accepted = true) {
    const writeText = vi.fn().mockResolvedValue(undefined);
    if (!accepted) writeText.mockRejectedValue(new Error('denied'));
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    return writeText;
}

const TWO_FENCES = [
    'Here is the helper:',
    '',
    '```go',
    'func main() {}',
    '```',
    '',
    'And how to run it:',
    '',
    '```sh',
    'make check',
    '```',
].join('\n');

function bubble(container: HTMLElement): Element {
    const text = container.querySelector('.assistant-bubble .message-text');
    if (!text) throw new Error('no assistant bubble rendered');
    return text;
}

function buttons(text: Element): HTMLButtonElement[] {
    return Array.from(text.querySelectorAll<HTMLButtonElement>('.code-fence .copy-btn'));
}

describe('a fenced block in an assistant message', () => {
    it('carries one copy control, inside the block it copies', () => {
        const { container } = render(ChatMessage, { role: 'assistant', content: '```go\npackage main\n```' });
        const [button] = buttons(bubble(container));
        expect(button).toBeDefined();
        expect(button.closest('.code-fence')?.querySelector('code')?.textContent).toBe('package main');
    });

    it('gives every fence its own control', () => {
        const { container } = render(ChatMessage, { role: 'assistant', content: TWO_FENCES });
        expect(buttons(bubble(container))).toHaveLength(2);
    });

    it('copies that block and nothing else', async () => {
        const writeText = stubClipboard();
        const { container } = render(ChatMessage, { role: 'assistant', content: TWO_FENCES });
        const [, second] = buttons(bubble(container));
        await fireEvent.click(second);
        expect(writeText).toHaveBeenCalledWith('make check');
    });

    it('shows the check while the clipboard holds it, then puts the copy icon back', async () => {
        vi.useFakeTimers();
        stubClipboard();
        const { container } = render(ChatMessage, { role: 'assistant', content: '```go\nx := 1\n```' });
        const [button] = buttons(bubble(container));
        expect(button.getAttribute('aria-label')).toBe('Copy code');

        await fireEvent.click(button);
        expect(button.classList.contains('copied')).toBe(true);
        expect(button.getAttribute('aria-label')).toBe('Copied');

        await vi.advanceTimersByTimeAsync(2000);
        expect(button.classList.contains('copied')).toBe(false);
        expect(button.getAttribute('aria-label')).toBe('Copy code');
    });

    it('says nothing when the clipboard refuses the write', async () => {
        stubClipboard(false);
        const { container } = render(ChatMessage, { role: 'assistant', content: '```go\nx := 1\n```' });
        const [button] = buttons(bubble(container));
        await fireEvent.click(button);
        expect(button.classList.contains('copied')).toBe(false);
        expect(button.getAttribute('aria-label')).toBe('Copy code');
    });

    it('keeps the block body byte for byte, indentation and all', () => {
        const { container } = render(ChatMessage, {
            role: 'assistant',
            content: ['```python', 'if x:', '    return 1', '```'].join('\n'),
        });
        expect(bubble(container).querySelector('.code-fence code')?.textContent).toBe('if x:\n    return 1');
    });

    it.each([
        ['a rendered markdown document', ['```markdown', '# Plan', '', '```go', 'inner()', '```', '```'].join('\n')],
        ['a fence inside a list item', ['- step', '', '  ```sh', '  ls', '  ```'].join('\n')],
        ['a fence with nothing to copy', '```go\n```'],
        ['an inline code span', 'Run `make check` now.'],
    ])('leaves %s without a control', (_name, content) => {
        const { container } = render(ChatMessage, { role: 'assistant', content });
        expect(buttons(bubble(container))).toEqual([]);
    });

    it('shows a fence holding markup as the text it is', () => {
        // Not vacuous: unescaped, this payload runs before any scanner could report it.
        const { container } = render(ChatMessage, {
            role: 'assistant',
            content: ['```html', '<img src=x onerror="steal()">', '```'].join('\n'),
        });
        const text = bubble(container);
        expect(htmlHazards(text)).toEqual([]);
        expect(text.querySelector('.code-fence img')).toBeNull();
        expect(text.querySelector('.code-fence code')?.textContent).toContain('onerror="steal()"');
    });

    it('renders a fence that is still open, with its control', () => {
        const { container } = render(ChatMessage, { role: 'assistant', content: 'here:\n\n```go\nfunc ma', isStreaming: true });
        expect(bubble(container).querySelector('.code-fence code')?.textContent).toBe('func ma');
        expect(buttons(bubble(container))).toHaveLength(1);
    });

    it('gives the user’s own fenced text no control', () => {
        const { container } = render(ChatMessage, { role: 'user', content: '```go\npackage main\n```' });
        expect(container.querySelectorAll('.copy-btn')).toHaveLength(0);
    });
});
