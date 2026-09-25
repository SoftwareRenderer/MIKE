// @vitest-environment jsdom
// Unit tests for the shared @-file-mention state machine.
// (.svelte.ts extension: the composable under test uses $state.)
import { describe, it, expect, vi } from 'vitest';
import { FileMention } from '../use-file-mention.svelte';

interface Harness {
    mention: FileMention;
    setValue: ReturnType<typeof vi.fn>;
    text: string;
    el: any;
}

function makeHarness(initial = ''): Harness {
    let text = initial;
    const el: any = {
        selectionStart: text.length,
        focus: vi.fn(),
        setSelectionRange: vi.fn(),
        getBoundingClientRect: () => ({ top: 10, bottom: 30, left: 5, width: 200 }),
        parentElement: { getBoundingClientRect: () => ({ top: 0, bottom: 40, left: 0, width: 400 }) },
    };
    const setValue = vi.fn((v: string) => { text = v; });
    const mention = new FileMention({
        getProjectId: () => 'proj-1',
        getValue: () => text,
        setValue,
        getTextarea: () => el,
    });
    return { mention, setValue, get text() { return text; }, el };
}

function key(k: string, opts: Partial<KeyboardEvent> = {}): KeyboardEvent {
    return { key: k, shiftKey: false, preventDefault: vi.fn(), stopPropagation: vi.fn(), ...opts } as unknown as KeyboardEvent;
}

describe('FileMention.handleKeydown', () => {
    it('ignores keys while closed', () => {
        const { mention } = makeHarness();
        const e = key('ArrowDown');
        expect(mention.handleKeydown(e)).toBe(false);
        expect(e.preventDefault).not.toHaveBeenCalled();
        // Escape outside an open dropdown belongs to the host (blur/close semantics)
        expect(mention.handleKeydown(key('Escape'))).toBe(false);
    });

    it('cycles the active row with ArrowDown/ArrowUp', () => {
        const { mention } = makeHarness();
        mention.trigger = { atIndex: 0, query: '' };
        mention.results = [{ path: 'a.ts' }, { path: 'b.ts' }, { path: 'c.ts' }];

        mention.handleKeydown(key('ArrowDown'));
        expect(mention.activeIndex).toBe(1);
        mention.handleKeydown(key('ArrowUp'));
        expect(mention.activeIndex).toBe(0);
        mention.handleKeydown(key('ArrowUp'));
        expect(mention.activeIndex).toBe(2); // wraps up into range
        mention.handleKeydown(key('ArrowDown'));
        expect(mention.activeIndex).toBe(0);
    });

    it('shift+Enter is left to the host (newline)', () => {
        const { mention } = makeHarness();
        mention.trigger = { atIndex: 0, query: '' };
        mention.results = [{ path: 'a.ts' }];
        expect(mention.handleKeydown(key('Enter', { shiftKey: true }))).toBe(false);
    });

    it('Escape while open is consumed, closes and stops propagation', () => {
        const { mention } = makeHarness();
        mention.trigger = { atIndex: 0, query: '' };
        mention.results = [{ path: 'a.ts' }];
        const e = key('Escape');
        expect(mention.handleKeydown(e)).toBe(true);
        expect(e.stopPropagation).toHaveBeenCalled();
        expect(mention.trigger).toBeNull();
    });
});

describe('FileMention.select', () => {
    it('replaces the trigger with @path and closes', () => {
        const h = makeHarness('look at @src/inde');
        h.mention.trigger = { atIndex: 8, query: 'src/inde' };
        h.mention.select('src/index.ts');
        expect(h.setValue).toHaveBeenCalledWith('look at @src/index.ts');
        expect(h.mention.trigger).toBeNull();
        expect(h.mention.results).toEqual([]);
    });

    it('does nothing without a trigger', () => {
        const h = makeHarness('plain text');
        h.mention.select('a.ts');
        expect(h.setValue).not.toHaveBeenCalled();
    });
});

describe('FileMention.handleInput', () => {
    it('arms the trigger and positions the dropdown over the textarea', () => {
        const h = makeHarness('see @read');
        h.mention.handleInput();
        expect(h.mention.trigger).toEqual({ atIndex: 4, query: 'read' });
        expect(h.mention.position).toEqual({ top: 10, bottom: 30, left: 5, width: 200 });
    });

    it('closes when the caret leaves the mention', () => {
        const h = makeHarness('no mention here');
        h.mention.trigger = { atIndex: 0, query: 'x' };
        h.mention.handleInput();
        expect(h.mention.trigger).toBeNull();
    });

    it('debounces the search and stores the results', async () => {
        vi.useFakeTimers();
        try {
            // The API layer reads the CSRF token before it puts a request on the wire, so the search is
            // picked out by its own path rather than by being the first call of the run.
            const searches: string[] = [];
            vi.stubGlobal('fetch', vi.fn(async (input: string) => {
                const url = new URL(String(input), 'http://localhost');
                const json = (body: unknown) => new Response(JSON.stringify(body), {
                    status: 200, headers: { 'Content-Type': 'application/json' },
                });
                if (url.pathname === '/api/auth/session') return json({ authenticated: true, csrf_token: 'tok' });
                searches.push(url.pathname + url.search);
                return json({ results: [{ path: 'src/readme.md' }] });
            }));
            const h = makeHarness('see @read');
            h.mention.handleInput();
            expect(searches).toEqual([]); // debounced
            await vi.advanceTimersByTimeAsync(200);
            expect(searches).toEqual(['/api/fs/search?q=read&project=proj-1']);
            expect(h.mention.results).toEqual([{ path: 'src/readme.md' }]);
        } finally {
            vi.unstubAllGlobals();
            vi.useRealTimers();
        }
    });
});
