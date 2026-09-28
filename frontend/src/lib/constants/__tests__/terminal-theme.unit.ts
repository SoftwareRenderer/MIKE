/**
 * The terminal's colours exist twice on purpose — `--color-terminal-*` in `app.css` for the panel
 * chrome, real colour strings for xterm, which paints its own buffer and cannot resolve a custom
 * property — so the two halves must be checked against each other.
 *
 * A fallback that drifts from its token is invisible until the stylesheet fails to load, and a theme
 * that retokenizes `--color-terminal-*` must move the terminal with it, which is the only reason the
 * read happens per terminal instead of once at import.
 */
import { describe, it, expect, afterEach } from 'vitest';
import { readFileSync } from 'fs';
import { join } from 'path';
import { terminalTheme } from '../terminal-theme';

/** vitest runs with cwd=frontend. */
const APP_CSS = join(process.cwd(), 'src', 'app.css');

/** `--color-terminal-bg: #1f2428;` → ['--color-terminal-bg', '#1f2428']. */
function declaredTokens(): Map<string, string> {
    return new Map(
        Array.from(readFileSync(APP_CSS, 'utf8').matchAll(/(--color-terminal-[a-z]+):\s*(#[0-9a-fA-F]{3,8});/g), (m) => [
            m[1],
            m[2].toLowerCase(),
        ]),
    );
}

describe('the xterm theme', () => {
    const root = document.documentElement;

    afterEach(() => {
        for (const name of declaredTokens().keys()) root.style.removeProperty(name);
    });

    it('falls back to the colour app.css declares for each token', () => {
        // No token resolves here, so what comes back is the module's own fallback — and it must be the
        // palette's value, or a terminal that boots before the stylesheet is a different terminal.
        const declared = declaredTokens();
        expect(declared.size).toBe(4);
        const theme = terminalTheme();
        expect((theme.background ?? '').toLowerCase()).toBe(declared.get('--color-terminal-bg'));
        expect((theme.foreground ?? '').toLowerCase()).toBe(declared.get('--color-terminal-fg'));
        expect((theme.cursor ?? '').toLowerCase()).toBe(declared.get('--color-terminal-cursor'));
        expect((theme.selectionBackground ?? '').toLowerCase()).toBe(declared.get('--color-terminal-selection'));
    });

    it('follows a theme that retokenizes the palette', () => {
        root.style.setProperty('--color-terminal-bg', '#010203');
        root.style.setProperty('--color-terminal-fg', '#040506');
        const theme = terminalTheme();
        expect(theme.background).toBe('#010203');
        expect(theme.foreground).toBe('#040506');
        // A token left alone keeps its fallback rather than going blank.
        expect(theme.cursor).not.toBe('');
        expect(theme.selectionBackground).not.toBe('');
    });
});
