import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';

// jsdom has no matchMedia and ChatMessage reads the viewport through useIsMobile.
window.matchMedia = vi.fn().mockImplementation((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
}));

// The API layer (and through it IndexedDB) is reached transitively; nothing here makes a request.
vi.mock('$lib/api', () => ({ getTaskMessageFull: vi.fn(), uploadTaskFile: vi.fn() }));

import ChatMessage from '$lib/components/Chat/ChatMessage.svelte';

afterEach(cleanup);

const PAUSE_SENTENCE = 'Task has been temporarily paused due to memory pressure, and will automatically resume';

describe('system note — a memory pause is drawn as a warning, not an error', () => {
    it('draws a severity="warning" system row in the warning style', () => {
        const { container } = render(ChatMessage, {
            role: 'system', severity: 'warning', kind: 'memory_pressure',
            content: 'Task has been temporarily paused due to memory pressure, and will automatically resume',
        });
        const card = container.querySelector('.system-card') as Element;
        expect(card.classList.contains('system-warning')).toBe(true);
        expect(card.classList.contains('system-info')).toBe(false);
    });

    it('words the pause from the catalog, ignoring the text stored on the row', () => {
        const { container } = render(ChatMessage, {
            role: 'system', severity: 'warning', kind: 'memory_pressure',
            content: 'Row text written by a server this catalog does not match',
        });
        const body = container.querySelector('.system-message') as Element;
        expect(body.textContent).toBe(PAUSE_SENTENCE);
    });

    it('keeps the auto-resume note in the quiet style and words it from the catalog', () => {
        const { container } = render(ChatMessage, {
            role: 'system', severity: 'info', kind: 'memory_resumed', content: 'resumed',
        });
        const card = container.querySelector('.system-card') as Element;
        expect(card.classList.contains('system-info')).toBe(true);
        expect(card.classList.contains('system-warning')).toBe(false);
        expect(container.querySelector('.system-message')?.textContent).toBe(
            'Memory freed up, so the task resumed automatically');
    });

    it('leaves an unlabelled system row in the default style with its own text', () => {
        const { container } = render(ChatMessage, {
            role: 'system', content: 'Merge conflict detected during commit — resolving with agent',
        });
        const card = container.querySelector('.system-card') as Element;
        expect(card.classList.contains('system-warning')).toBe(false);
        expect(card.classList.contains('system-info')).toBe(false);
        expect(container.querySelector('.system-message')?.textContent).toContain('Merge conflict detected');
    });

    it('draws a merge note stored with severity="warning" in the warning style too', () => {
        const { container } = render(ChatMessage, {
            role: 'system', severity: 'warning', content: 'Merge aborted — the target branch moved',
        });
        expect((container.querySelector('.system-card') as Element).classList.contains('system-warning')).toBe(true);
    });
});
