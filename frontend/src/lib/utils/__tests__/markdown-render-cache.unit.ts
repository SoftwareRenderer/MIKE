/**
 * A streaming answer re-renders the paragraphs it finished long ago on every flush; these hold the
 * memo that stops that. The sanitizing itself is `sanitize.unit.ts`'s.
 */
import { describe, it, expect, vi, afterEach } from 'vitest';
import DOMPurify from 'dompurify';
import { markdownToSafeHTML, MARKDOWN_RENDER_CACHE_MAX } from '../sanitize';

/** How many times the sanitizer ran across `run`. */
function sanitizeCalls(run: () => void): number {
    const spy = vi.spyOn(DOMPurify, 'sanitize');
    run();
    const calls = spy.mock.calls.length;
    spy.mockRestore();
    return calls;
}

describe('markdownToSafeHTML', () => {
    afterEach(() => {
        vi.restoreAllMocks();
    });

    it('renders a source run it has already rendered without sanitizing again', () => {
        const run = 'A paragraph that finished streaming a while ago.\n\n- one\n- two';
        const first = markdownToSafeHTML(run);

        const repeats = sanitizeCalls(() => {
            for (let i = 0; i < 20; i++) {
                // Every flush of a streaming message asks for this run again.
                expect(markdownToSafeHTML(run)).toBe(first);
            }
        });

        expect(repeats).toBe(0);
    });

    it('renders two different runs separately and keeps the hazard refusals', () => {
        const hazard = markdownToSafeHTML('# Head <img src=x onerror=alert(1)>');

        expect(hazard).toContain('<h1');
        expect(hazard).not.toContain('onerror');
        // A cached answer for one run is never the answer for another.
        expect(markdownToSafeHTML('# Head')).not.toBe(hazard);
    });

    it('holds no more than the bound, oldest run first', () => {
        const oldest = `run that will be pushed out ${Math.random()}`;
        markdownToSafeHTML(oldest);

        // Past the bound nothing old survives, so the only claim left is that a run evicted by it
        // renders again.
        const pushes = () => {
            for (let i = 0; i < MARKDOWN_RENDER_CACHE_MAX + 5; i++) markdownToSafeHTML(`run ${Math.random()}`);
        };
        expect(sanitizeCalls(pushes)).toBe(MARKDOWN_RENDER_CACHE_MAX + 5);
        expect(sanitizeCalls(() => markdownToSafeHTML(oldest))).toBe(1);
    });
});
