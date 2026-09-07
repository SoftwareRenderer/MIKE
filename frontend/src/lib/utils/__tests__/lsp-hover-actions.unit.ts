import { describe, it, expect } from 'vitest';
import {
    reduceHover,
    HOVER_IDLE,
    sameToken,
    hoverDocsHTML,
    isJumpModifier,
    resolveHoverTimings,
    DEFAULT_HOVER_TIME,
    DEFAULT_HOVER_GRACE,
    HoverCard,
    type HoverEffect,
    type HoverInput,
    type HoverMachineState,
    type HoverZone,
} from '../lsp-hover-actions';
import type { DefinitionTarget } from '../lsp-definition';
import type { LSPPlugin } from '@codemirror/lsp-client';

/**
 * These transitions are the whole reason the hover card is not a plain
 * `hoverTooltip`: CodeMirror's hover plugin dismisses the moment the pointer
 * leaves the hovered character, which is the exact motion needed to press the
 * card's "Go to Definition" button. `reduceHover` is the decision the plugin
 * runs, so the click-stability contract is testable without a browser.
 */

const TOKEN_A = { from: 10, to: 14 };
const TOKEN_B = { from: 20, to: 26 };

/** Apply a sequence of inputs, returning the final state and every effect fired. */
function run(inputs: HoverInput[], start: HoverMachineState = HOVER_IDLE) {
    let state = start;
    const effects: HoverEffect[] = [];
    for (const input of inputs) {
        const decision = reduceHover(state, input);
        state = decision.state;
        effects.push(...decision.effects);
    }
    return { state, effects };
}

const openOnA = (): HoverMachineState =>
    run([{ type: 'pointermove', zone: 'token', token: TOKEN_A }, { type: 'hover-elapsed' }]).state;

describe('reduceHover — opening', () => {
    it('arms a timer when the pointer settles on an identifier', () => {
        const { state, effects } = run([{ type: 'pointermove', zone: 'token', token: TOKEN_A }]);
        expect(state.phase).toBe('arming');
        expect(state.token).toEqual(TOKEN_A);
        expect(effects).toContain('arm');
    });

    it('opens nothing until the hover delay elapses', () => {
        expect(run([{ type: 'pointermove', zone: 'token', token: TOKEN_A }]).state.phase).toBe('arming');
    });

    it('opens the card when the hover delay elapses', () => {
        const { state, effects } = run([{ type: 'pointermove', zone: 'token', token: TOKEN_A }, { type: 'hover-elapsed' }]);
        expect(state.phase).toBe('open');
        expect(effects).toContain('open');
    });

    it('ignores a timer that fired while idle', () => {
        const { state, effects } = run([{ type: 'hover-elapsed' }]);
        expect(state).toBe(HOVER_IDLE);
        expect(effects).toEqual([]);
    });

    it('does not arm when the pointer is not on an identifier', () => {
        expect(run([{ type: 'pointermove', zone: 'outside', token: null }]).state).toBe(HOVER_IDLE);
    });
});

describe('reduceHover — dismissing', () => {
    it('cancels the hover when the pointer leaves before the card opens', () => {
        const { state, effects } = run([
            { type: 'pointermove', zone: 'token', token: TOKEN_A },
            { type: 'pointermove', zone: 'outside', token: null },
            { type: 'hover-elapsed' },
        ]);
        expect(state).toBe(HOVER_IDLE);
        expect(effects).toContain('cancel-arm');
        expect(effects).not.toContain('open');
    });

    it('stays open while the pointer is on the identifier', () => {
        const { state, effects } = run(
            [{ type: 'pointermove', zone: 'token', token: TOKEN_A }],
            openOnA(),
        );
        expect(state.phase).toBe('open');
        expect(effects).toEqual([]);
    });

    it('stays open while the pointer is inside the card', () => {
        const { state, effects } = run(
            [{ type: 'pointermove', zone: 'tooltip', token: null }],
            openOnA(),
        );
        expect(state.phase).toBe('open');
        expect(effects).toEqual([]);
    });

    it('gives the pointer a grace period after it leaves both', () => {
        const { state, effects } = run(
            [{ type: 'pointermove', zone: 'outside', token: null }],
            openOnA(),
        );
        expect(state.phase).toBe('open');
        expect(state.gracePending).toBe(true);
        expect(effects).toEqual(['grace']);
    });

    it('closes only once the grace period has elapsed', () => {
        const { state, effects } = run(
            [{ type: 'pointermove', zone: 'outside', token: null }, { type: 'grace-elapsed' }],
            openOnA(),
        );
        expect(state).toBe(HOVER_IDLE);
        expect(effects).toEqual(['grace', 'close']);
    });

    it('cancels the close when the pointer comes back to the card in time', () => {
        const { state } = run(
            [
                { type: 'pointermove', zone: 'outside', token: null },
                { type: 'pointermove', zone: 'tooltip', token: null },
                { type: 'grace-elapsed' },
            ],
            openOnA(),
        );
        expect(state.phase).toBe('open');
    });

    it('re-dwells when the pointer comes back to the identifier instead', () => {
        // The grace is for the trip to the card, not the identifier: back on the symbol, the card has to
        // be earned again.
        const { state, effects } = run(
            [
                { type: 'pointermove', zone: 'outside', token: null },
                { type: 'pointermove', zone: 'token', token: TOKEN_A },
            ],
            openOnA(),
        );
        expect(effects).toContain('close');
        expect(effects).toContain('cancel-grace');
        expect(effects.filter(e => e === 'arm')).toHaveLength(1);
        expect(state).toEqual({ phase: 'arming', token: TOKEN_A, gracePending: false });
    });

    it('does not stack grace timers', () => {
        const { effects } = run(
            [
                { type: 'pointermove', zone: 'outside', token: null },
                { type: 'pointermove', zone: 'outside', token: null },
            ],
            openOnA(),
        );
        expect(effects.filter(e => e === 'grace')).toHaveLength(1);
    });

    it('closes on a document change', () => {
        const { state, effects } = run([{ type: 'doc-change' }], openOnA());
        expect(state).toBe(HOVER_IDLE);
        expect(effects).toContain('close');
    });

    it('closes when the card has nothing to show', () => {
        const { state, effects } = run([{ type: 'dismiss' }], openOnA());
        expect(state).toBe(HOVER_IDLE);
        expect(effects).toContain('close');
    });
});

describe('reduceHover — moving between identifiers', () => {
    it('closes the card at once when the pointer reaches another identifier', () => {
        const { state, effects } = run(
            [{ type: 'pointermove', zone: 'other-token', token: TOKEN_B }],
            openOnA(),
        );
        // Close, then dwell. Re-targeting in place (the old contract) made the hover delay something a
        // session paid once.
        expect(state.phase).toBe('arming');
        expect(state.token).toEqual(TOKEN_B);
        expect(effects).toContain('close');
        expect(effects).toContain('arm');
        expect(effects).not.toContain('open');
    });

    it('shows the next identifier only after a dwell of its own', () => {
        const { effects } = run(
            [{ type: 'pointermove', zone: 'other-token', token: TOKEN_B }, { type: 'hover-elapsed' }],
            openOnA(),
        );
        expect(effects.filter(e => e === 'open')).toHaveLength(1);
        expect(effects.filter(e => e === 'close')).toHaveLength(1);
    });

    it('re-arms for every identifier crossed and opens once, at the last', () => {
        const TOKEN_C = { from: 30, to: 34 };
        const { state, effects } = run(
            [
                { type: 'pointermove', zone: 'other-token', token: TOKEN_B },
                { type: 'pointermove', zone: 'other-token', token: TOKEN_C },
                { type: 'hover-elapsed' },
            ],
            openOnA(),
        );
        // So a slow sweep across a dense line shows nothing until the pointer stops: one close, one arm
        // per identifier it rested on, one open.
        expect(effects.filter(e => e === 'close')).toHaveLength(1);
        expect(effects.filter(e => e === 'arm')).toHaveLength(2);
        expect(state.phase).toBe('open');
        expect(state.token).toEqual(TOKEN_C);
    });

    it('does not restart the dwell for a wiggle inside the identifier', () => {
        // mousemove fires for a single pixel; treating that as a new hover would make the dwell
        // unreachable.
        const { state, effects } = run(
            [
                { type: 'pointermove', zone: 'token', token: TOKEN_A },
                { type: 'pointermove', zone: 'token', token: TOKEN_A },
                { type: 'pointermove', zone: 'token', token: TOKEN_A },
            ],
            openOnA(),
        );
        expect(effects).toEqual([]);
        expect(state.phase).toBe('open');
        expect(state.token).toEqual(TOKEN_A);
    });

    it('drops the pending close as soon as it closes the card itself', () => {
        const { effects } = run(
            [
                { type: 'pointermove', zone: 'outside', token: null },
                { type: 'pointermove', zone: 'other-token', token: TOKEN_B },
                { type: 'grace-elapsed' },
            ],
            openOnA(),
        );
        // Cancelled, not raced: a grace timer must not fire later and close a card that has not opened.
        expect(effects).toContain('cancel-grace');
        expect(effects.filter(e => e === 'close')).toHaveLength(1);
    });

    it('re-arms for the identifier actually under the pointer while arming', () => {
        const { state, effects } = run([
            { type: 'pointermove', zone: 'token', token: TOKEN_A },
            { type: 'pointermove', zone: 'other-token', token: TOKEN_B },
            { type: 'hover-elapsed' },
        ]);
        expect(state.phase).toBe('open');
        expect(state.token).toEqual(TOKEN_B);
        expect(effects.filter(e => e === 'arm')).toHaveLength(2);
    });

    it('carries the close-grace only on an open state', () => {
        // Why `rearm` emits no `cancel-grace` when nothing is open: only an open state can have a grace
        // timer running. Walked out of every reachable state rather than assumed.
        const zones: HoverZone[] = ['token', 'tooltip', 'other-token', 'outside'];
        const tokens = [null, TOKEN_A, TOKEN_B];
        const seen = new Set<string>();
        const queue: HoverMachineState[] = [HOVER_IDLE];
        while (queue.length) {
            const state = queue.pop()!;
            const inputs: HoverInput[] = [
                { type: 'hover-elapsed' },
                { type: 'grace-elapsed' },
                { type: 'scroll', zone: 'outside' },
                { type: 'scroll', zone: 'tooltip' },
                { type: 'doc-change' },
                { type: 'dismiss' },
                ...zones.flatMap((zone) => tokens.map((token) => ({ type: 'pointermove' as const, zone, token }))),
            ];
            for (const input of inputs) {
                const next = reduceHover(state, input).state;
                expect(
                    next.phase === 'open' || !next.gracePending,
                    `${state.phase} --${input.type}--> ${next.phase} with gracePending`,
                ).toBe(true);
                const key = `${next.phase}|${next.token?.from ?? 'null'}|${next.gracePending}`;
                if (!seen.has(key)) {
                    seen.add(key);
                    queue.push(next);
                }
            }
        }
        expect([...seen].filter((k) => k.endsWith('|true')).sort()).toEqual(['open|10|true', 'open|20|true']);
    });

    it('knows two ranges are the same token', () => {
        expect(sameToken({ from: 1, to: 3 }, { from: 1, to: 3 })).toBe(true);
        expect(sameToken({ from: 1, to: 3 }, { from: 1, to: 4 })).toBe(false);
        expect(sameToken(null, { from: 1, to: 3 })).toBe(false);
    });
});

describe('reduceHover — scrolling', () => {
    it('closes on a scroll the pointer is not inside', () => {
        const { state, effects } = run([{ type: 'scroll', zone: 'outside' }], openOnA());
        expect(state).toBe(HOVER_IDLE);
        expect(effects).toContain('close');
    });

    it('survives a scroll that starts inside the card', () => {
        const { state, effects } = run([{ type: 'scroll', zone: 'tooltip' }], openOnA());
        expect(state.phase).toBe('open');
        expect(effects).toEqual([]);
    });

    it('closes on scroll while arming, so a stale timer cannot open a card', () => {
        const { state, effects } = run([
            { type: 'pointermove', zone: 'token', token: TOKEN_A },
            { type: 'scroll', zone: 'outside' },
            { type: 'hover-elapsed' },
        ]);
        expect(state).toBe(HOVER_IDLE);
        expect(effects).not.toContain('open');
    });
});

describe('hoverDocsHTML', () => {
    // Stands in for the language-server renderer: plaintext is escaped,
    // markdown is passed through (marked in the real implementation).
    const plugin = {
        docToHTML: (value: unknown, kind?: string) => {
            const text = typeof value === 'string' ? value : String((value as { value?: string })?.value ?? '');
            const effective = typeof value === 'object' && value && 'kind' in value ? (value as { kind: string }).kind : kind;
            return effective === 'plaintext' ? text.replace(/</g, '&lt;') : text;
        },
    } as unknown as LSPPlugin;

    it('escapes plaintext documentation', () => {
        const html = hoverDocsHTML(plugin, { contents: { kind: 'plaintext', value: 'a < b' } });
        expect(html).toBe('a &lt; b');
    });

    it('wraps a MarkedString in a fenced code block', () => {
        const html = hoverDocsHTML(plugin, { contents: { language: 'go', value: 'func main()' } });
        expect(html).toContain('```go\nfunc main()\n```');
    });

    it('joins an array of marked strings', () => {
        const html = hoverDocsHTML(plugin, { contents: ['one', { language: 'go', value: 'two' }] });
        expect(html).toContain('one');
        expect(html).toContain('<br>');
    });

    it('sanitizes markup the language server embedded in markdown', () => {
        const html = hoverDocsHTML(plugin, {
            contents: { kind: 'markdown', value: 'docs <img src=x onerror=alert(1)><script>steal()</script>' },
        });
        expect(html).not.toContain('<script');
        expect(html).not.toContain('onerror');
    });

    it('drops the style a server could paint the card over the app with', () => {
        // The card is appended inside the application, so a fixed-position box
        // here would sit above the real UI rather than inside the tooltip.
        const html = hoverDocsHTML(plugin, {
            contents: { kind: 'markdown', value: '<p style="position:fixed;inset:0">confirm?</p>func F()' },
        });
        expect(html).not.toMatch(/style/i);
        expect(html).toContain('confirm?');
        expect(html).toContain('func F()');
    });

    it('drops a style element as well as the style attribute', () => {
        // Same layout power, different syntax — and the shape DOMPurify's defaults keep
        // when the element is nested in something, which is how a doc comment delivers it.
        const html = hoverDocsHTML(plugin, {
            contents: { kind: 'markdown', value: '<div><style>.cm-hoverCard{position:fixed;inset:0}</style></div>func F()' },
        });
        expect(html).not.toMatch(/<style|position:fixed/i);
        expect(html).toContain('func F()');
    });

    it('drops the rules a server puts between sections, keeping the sections', () => {
        // A real server emits these as markdown `---`, which its renderer turns
        // into <hr> — the stub above does no markdown, so ask for the tag.
        const html = hoverDocsHTML(plugin, {
            contents: { kind: 'markdown', value: 'func Documented() int<hr>Does something useful.' },
        });
        expect(html).not.toContain('<hr');
        expect(html).toContain('func Documented() int');
        expect(html).toContain('Does something useful.');
    });

    it('returns nothing when the server sent no contents', () => {
        expect(hoverDocsHTML(plugin, null)).toBe('');
        expect(hoverDocsHTML(plugin, {})).toBe('');
    });
});

describe('isJumpModifier', () => {
    const click = (over: Partial<{ ctrlKey: boolean; metaKey: boolean; shiftKey: boolean; altKey: boolean }>) => ({
        ctrlKey: false,
        metaKey: false,
        shiftKey: false,
        altKey: false,
        ...over,
    });

    it('jumps on Ctrl or Cmd', () => {
        expect(isJumpModifier(click({ ctrlKey: true }))).toBe(true);
        expect(isJumpModifier(click({ metaKey: true }))).toBe(true);
    });

    it('is a plain click otherwise', () => {
        expect(isJumpModifier(click({}))).toBe(false);
    });

    it('leaves Shift and Alt to selection and multi-cursor', () => {
        expect(isJumpModifier(click({ ctrlKey: true, shiftKey: true }))).toBe(false);
        expect(isJumpModifier(click({ metaKey: true, altKey: true }))).toBe(false);
    });
});

describe('resolveHoverTimings', () => {
    it('waits half a second over the symbol before anything appears', () => {
        // The complaint this pins down: a card that fires the moment the mouse
        // crosses a symbol interrupts a read of the code.
        expect(DEFAULT_HOVER_TIME).toBe(500);
        expect(resolveHoverTimings({})).toEqual({ hoverTime: 500, graceMs: DEFAULT_HOVER_GRACE });
    });

    it('honours an explicit timing, including a zero delay', () => {
        expect(resolveHoverTimings({ hoverTime: 0, graceMs: 120 })).toEqual({ hoverTime: 0, graceMs: 120 });
    });
});

describe('HoverCard — where the button lives', () => {
    const TARGET: DefinitionTarget = { path: '/repo/target.go', line: 3 };

    it('keeps the action row out of the scrolling documentation body', () => {
        const card = new HoverCard(() => {});
        const body = card.dom.querySelector('.cm-lsp-action-card-body') as HTMLElement;
        const row = card.dom.querySelector('.cm-lsp-action-row') as HTMLElement;
        // The card caps itself and the body scrolls, so a row rendered *inside*
        // the body would slide out of view with the documentation. As a sibling
        // of a flex-shrink: 0 body it stays on the bottom edge.
        expect(row.parentElement).toBe(card.dom);
        expect(body.contains(row)).toBe(false);
    });

    it('shows the button only for a target, and jumps with it', () => {
        const jumps: DefinitionTarget[] = [];
        const card = new HoverCard(target => jumps.push(target));
        const row = card.dom.querySelector('.cm-lsp-action-row') as HTMLElement;
        const button = card.dom.querySelector('.cm-lsp-action-button') as HTMLButtonElement;

        expect(row.hidden).toBe(true);
        card.showAction(TARGET);
        expect(row.hidden).toBe(false);

        button.click();
        expect(jumps).toEqual([TARGET]);
    });

    it('fills documentation without disturbing the action row', () => {
        const card = new HoverCard(() => {});
        card.showDocs('<p>docs</p>');
        card.showAction(TARGET);
        const docs = card.dom.querySelector('.cm-lsp-action-docs') as HTMLElement;
        expect(docs.hidden).toBe(false);
        expect(docs.innerHTML).toContain('docs');
        expect(card.dom.querySelector('.cm-lsp-action-loading')!.hasAttribute('hidden')).toBe(true);
        // Documentation grows inside the body, above the row — never next to it.
        expect(card.dom.lastElementChild!.className).toContain('cm-lsp-action-row');
    });
});
