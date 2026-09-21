import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';

// These components must render a ```markdown block as markup rather than showing its source.
// `ChatMessage` memoizes its render, so the helper is proved here as well as in `sanitize.unit.ts`.

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
import ToolCall from '$lib/components/Chat/ToolCall.svelte';
import { htmlHazards } from '$lib/utils/__tests__/html-hazards';

afterEach(cleanup);

const BLOCK = ['Read this:', '', '```markdown', '# Plan', '', '- **do** the thing', '```', '', 'End.'].join('\n');

const toolCallBase = {
    expandedArgs: new Set<string>(),
    editDiffOverrides: {},
    onToggleArgs: vi.fn(),
    onToggleEditDiff: vi.fn(),
};

describe('a markdown block in a rendered message', () => {
    it('reaches the assistant bubble as markup', () => {
        const { container } = render(ChatMessage, { role: 'assistant', content: BLOCK });
        const text = container.querySelector('.assistant-bubble .message-text') as Element;
        expect(text.querySelector('h1')?.textContent).toBe('Plan');
        expect(text.querySelector('strong')?.textContent).toBe('do');
        // The block is part of the message, not a code island beside it.
        expect(text.querySelector('pre')).toBeNull();
        expect(text.textContent).toContain('End.');
    });

    it('leaves a fence in another language as source', () => {
        // Only the `markdown` tag renders, so a model can still show markup it means the reader to copy.
        const { container } = render(ChatMessage, {
            role: 'assistant',
            content: '```go\npackage main\n```',
        });
        const text = container.querySelector('.assistant-bubble .message-text') as Element;
        expect(text.querySelector('pre code')?.textContent).toContain('package main');
    });

    it.each([
        ['a stylesheet', '<div><style>.card{display:none}</style>Hidden prose</div>', 'Hidden prose'],
        ['an end tag that would close the bubble', '</div><img src=x onerror=alert(1)>after', 'after'],
        ['a script tag', '<script>steal()</script><p>keep me</p>', 'keep me'],
    ])('keeps %s out of the bubble while rendering the block', (_name, payload, words) => {
        const { container } = render(ChatMessage, {
            role: 'assistant',
            content: ['```markdown', payload, '```'].join('\n'),
        });
        const text = container.querySelector('.assistant-bubble .message-text') as Element;
        // An unbalanced end tag in a fenced document cannot restructure the component.
        expect(text, 'the bubble still wraps the rendered block').not.toBeNull();
        // Not vacuous: unrendered, the payload is escaped text and inert for the wrong reason.
        expect(text.querySelector('pre')).toBeNull();
        expect(htmlHazards(text)).toEqual([]);
        expect(text.textContent).toContain(words);
    });

    it('renders one in a request_review summary', () => {
        const { container } = render(ToolCall, {
            ...toolCallBase,
            toolCall: {
                toolId: 'tool-1',
                toolName: 'request_review',
                args: { summary: BLOCK },
                result: undefined,
                success: true,
                isPending: false,
                sandbox: false,
                resultTruncated: false,
            },
        });
        const summary = container.querySelector('.tool-result-summary') as Element;
        expect(summary.querySelector('h1')?.textContent).toBe('Plan');
        expect(summary.querySelector('pre')).toBeNull();
    });
});
