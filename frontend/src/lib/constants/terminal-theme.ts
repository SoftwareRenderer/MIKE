// =============================================================================
// The xterm theme — the data half of the `--color-terminal-*` tokens in `src/app.css`
//
// xterm paints its buffer itself, so the theme object must carry real colour strings rather than the
// `var()` references CSS uses; this read is the only place the terminal's colours are chosen.
// `__tests__/terminal-theme.unit.ts` reads `app.css` and fails if a fallback below drifts from the
// token named beside it, so the comments are pointers, not the only guard.
//
// The ANSI 16 stay unset: a program's escape codes were written against xterm's own palette, so
// retinting those would make `ls --color` report a colour it did not ask for.
// =============================================================================

import type { ITheme } from '@xterm/xterm';

/** Fallback colours, each mirroring the `app.css` token named beside it. */
const FALLBACK = {
    bg: '#1F2428',        // --color-terminal-bg
    fg: '#E6EDF3',        // --color-terminal-fg
    cursor: '#0084FF',    // --color-terminal-cursor
    selection: '#3E464E', // --color-terminal-selection
};

/** The xterm theme, read off the element the palette's custom properties are declared on. */
export function terminalTheme(root: HTMLElement = document.documentElement): ITheme {
    const styles = getComputedStyle(root);
    const token = (name: string, fallback: string): string =>
        styles.getPropertyValue(name).trim() || fallback;

    return {
        background: token('--color-terminal-bg', FALLBACK.bg),
        foreground: token('--color-terminal-fg', FALLBACK.fg),
        cursor: token('--color-terminal-cursor', FALLBACK.cursor),
        selectionBackground: token('--color-terminal-selection', FALLBACK.selection),
    };
}
