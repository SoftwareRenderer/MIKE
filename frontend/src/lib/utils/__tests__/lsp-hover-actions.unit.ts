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
    it('re-targets instead of closing when the pointer reaches another identifier', () => {
        const { state, effects } = run(
            [{ type: 'pointermove', zone: 'other-token', token: TOKEN_B }],
            openOnA(),
        );
        expect(state.phase).toBe('open');
        expect(state.token).toEqual(TOKEN_B);
        expect(effects).toContain('retarget');
        expect(effects).not.toContain('close');
    });

    it('drops a pending close when it re-targets', () => {
        const { effects } = run(
            [
                { type: 'pointermove', zone: 'outside', token: null },
                { type: 'pointermove', zone: 'other-token', token: TOKEN_B },
                { type: 'grace-elapsed' },
            ],
            openOnA(),
        );
        expect(effects).toContain('cancel-grace');
        expect(effects).not.toContain('close');
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
