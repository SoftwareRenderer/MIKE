import { describe, it, expect, vi, afterEach, beforeAll } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';

// A closed ```mermaid fence in an assistant bubble is drawn; an open one, a nested one and a message the
// user wrote are not. The renderer is the real one, so the bubble is proved against real bytes.

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

import { stubSvgMeasurement } from '$lib/utils/__tests__/svg-measurement';
import { renderDiagram } from '$lib/utils/diagram';
import ChatMessage from '$lib/components/Chat/ChatMessage.svelte';

// The first diagram of a run pays for the lazy renderer chunk, so the waits below are not timeouts
// tuned to a warm-up.
beforeAll(async () => {
    stubSvgMeasurement();
    await renderDiagram('flowchart TD\n  A[warm up] --> B[the renderer]');
}, 60_000);

afterEach(() => {
    cleanup();
    delete (navigator as { clipboard?: unknown }).clipboard;
});

const DIAGRAM = ['```mermaid', 'flowchart TD', '  A[Alpha] --> B[Beta]', '```'].join('\n');

function bubble(container: HTMLElement): Element {
    const text = container.querySelector('.assistant-bubble .message-text');
    if (!text) throw new Error('no assistant bubble rendered');
    return text;
}

const image = (scope: Element) => scope.querySelector('img.diagram');
const source = (scope: Element) => scope.querySelector('.diagram-fence pre');
const tabs = (scope: Element) => Array.from(scope.querySelectorAll<HTMLButtonElement>('.fence-tabs .tab'));

function stubClipboard(): { writes: string[] } {
    const writes: string[] = [];
    Object.defineProperty(navigator, 'clipboard', {
        value: { writeText: (text: string) => { writes.push(text); return Promise.resolve(); } },
        configurable: true,
    });
    return { writes };
}

describe('a fenced diagram in an assistant message', () => {
    it('is drawn as an image of itself', async () => {
        const { container } = render(ChatMessage, { role: 'assistant', content: DIAGRAM });
        await waitFor(() => expect(image(bubble(container))).not.toBeNull());
        expect(image(bubble(container))?.getAttribute('src')).toMatch(/^data:image\/svg\+xml;base64,/);
    });

    it('switches between the picture and the text it came from', async () => {
        const { container } = render(ChatMessage, { role: 'assistant', content: DIAGRAM });
        await waitFor(() => expect(image(bubble(container))).not.toBeNull());
        const [picture, text] = tabs(bubble(container));
        expect(picture?.getAttribute('aria-pressed')).toBe('true');

        await fireEvent.click(text);
        expect(source(bubble(container))?.textContent).toContain('flowchart TD');
        expect(image(bubble(container))).toBeNull();

        await fireEvent.click(picture);
        expect(image(bubble(container))).not.toBeNull();
    });

    it('copies whichever of the two is on screen', async () => {
        const { writes } = stubClipboard();
        const { container } = render(ChatMessage, { role: 'assistant', content: DIAGRAM });
        await waitFor(() => expect(image(bubble(container))).not.toBeNull());
        const copy = bubble(container).querySelector<HTMLButtonElement>('.fence-tabs .copy-btn');

        await fireEvent.click(copy!);
        expect(writes.at(-1)?.startsWith('<svg')).toBe(true);

        await fireEvent.click(tabs(bubble(container))[1]);
        await fireEvent.click(bubble(container).querySelector<HTMLButtonElement>('.fence-tabs .copy-btn')!);
        expect(writes.at(-1)).toContain('flowchart TD');
        expect(writes.at(-1)?.startsWith('<svg')).toBe(false);
    });

    it('waits for the delimiter instead of drawing half a diagram', async () => {
        const { container } = render(ChatMessage, {
            role: 'assistant',
            content: 'here:\n\n```mermaid\nflowchart TD\n  A[Alpha] -->',
            isStreaming: true,
        });
        expect(bubble(container).querySelector('pre code')?.textContent).toContain('flowchart TD');
        expect(image(bubble(container))).toBeNull();
        expect(tabs(bubble(container))).toEqual([]);
    });

    it('shows the text with a word about it when the source is not a diagram', async () => {
        const { container } = render(ChatMessage, {
            role: 'assistant',
            content: '```mermaid\nthis is not a diagram at all\n```',
        });
        await waitFor(() =>
            expect(bubble(container).querySelector('.note')?.textContent).toContain('could not be rendered'));
        expect(image(bubble(container))).toBeNull();
        expect(tabs(bubble(container))).toEqual([]);
        expect(source(bubble(container))?.textContent).toContain('this is not a diagram at all');
    });

    it.each([
        ['inside a list item', ['- step', '', '  ```mermaid', '  flowchart TD', '  A-->B', '  ```'].join('\n'), 'flowchart'],
        ['with nothing in it', 'before\n\n```mermaid\n```\n\nafter', 'before'],
    ])('leaves a diagram %s to the prose', async (_name, content, marker) => {
        const { container } = render(ChatMessage, { role: 'assistant', content });
        await waitFor(() => expect(bubble(container).textContent).toContain(marker));
        expect(image(bubble(container))).toBeNull();
        expect(bubble(container).querySelector('.diagram-fence')).toBeNull();
    });

    it('draws nothing for the user’s own fenced text', async () => {
        const { container } = render(ChatMessage, { role: 'user', content: DIAGRAM });
        expect(container.querySelector('.diagram-fence')).toBeNull();
        expect(container.querySelector('img.diagram')).toBeNull();
    });
});
