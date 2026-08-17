import { describe, it, expect, vi, afterEach } from 'vitest';
import {
    DEFAULT_SHORTCUTS,
    normalizeKey,
    formatShortcut,
    parseShortcut,
    matchShortcut,
    effectiveCreateTaskKey,
    effectiveOpenTerminalKey,
    isMac,
    defaultCreateTaskSubmitKey,
    effectiveCreateTaskSubmitKey,
    matchesCreateTaskSubmitKey,
    defaultStartTaskKey,
    effectiveStartTaskKey,
    effectiveToggleFullscreenKey,
    fullscreenToggleTitle,
    shortcutHintsHidden,
} from '$lib/utils/keyboard';
import type { Config } from '$lib/types';

const MAC_UA = 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36';
const WIN_UA = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36';

function mockUserAgent(ua: string) {
    vi.spyOn(navigator, 'userAgent', 'get').mockReturnValue(ua);
}

afterEach(() => {
    vi.restoreAllMocks();
});

function keyEvent(init: Partial<KeyboardEventInit> & { key: string }): KeyboardEvent {
    return new KeyboardEvent('keydown', { cancelable: true, ...init });
}

describe('normalizeKey', () => {
    it('normalizes a plain letter to its lowercase form', () => {
        expect(normalizeKey(keyEvent({ key: 'c' }))).toBe('c');
        expect(normalizeKey(keyEvent({ key: 'C', shiftKey: true }))).toBe('shift+c');
    });

    it('normalizes digits and symbols', () => {
        expect(normalizeKey(keyEvent({ key: '5' }))).toBe('5');
        expect(normalizeKey(keyEvent({ key: '@', shiftKey: true }))).toBe('shift+@');
    });

    it('normalizes modifier combos in canonical order (control, meta, shift, alt)', () => {
        expect(normalizeKey(keyEvent({ key: 'k', ctrlKey: true }))).toBe('control+k');
        expect(normalizeKey(keyEvent({ key: 'k', metaKey: true, shiftKey: true }))).toBe('meta+shift+k');
        expect(normalizeKey(keyEvent({ key: 'k', altKey: true }))).toBe('alt+k');
    });

    it('rejects modifier-only presses', () => {
        expect(normalizeKey(keyEvent({ key: 'Control', ctrlKey: true }))).toBeNull();
        expect(normalizeKey(keyEvent({ key: 'Shift', shiftKey: true }))).toBeNull();
    });

    it('rejects named keys without modifiers (F-keys, arrows, space, enter)', () => {
        expect(normalizeKey(keyEvent({ key: 'F5' }))).toBeNull();
        expect(normalizeKey(keyEvent({ key: 'ArrowUp' }))).toBeNull();
        expect(normalizeKey(keyEvent({ key: ' ' }))).toBeNull();
        expect(normalizeKey(keyEvent({ key: 'Enter' }))).toBeNull();
    });

    it('accepts named keys with modifiers', () => {
        expect(normalizeKey(keyEvent({ key: 'F5', ctrlKey: true }))).toBe('control+f5');
        expect(normalizeKey(keyEvent({ key: 'ArrowUp', shiftKey: true }))).toBe('shift+arrowup');
    });

    it('rejects Escape (reserved for canceling capture)', () => {
        expect(normalizeKey(keyEvent({ key: 'Escape' }))).toBeNull();
        expect(normalizeKey(keyEvent({ key: 'Escape', shiftKey: true }))).toBeNull();
    });

    it('rejects browser-reserved combos', () => {
        expect(normalizeKey(keyEvent({ key: 'w', ctrlKey: true }))).toBeNull();
        expect(normalizeKey(keyEvent({ key: 'q', ctrlKey: true }))).toBeNull();
        expect(normalizeKey(keyEvent({ key: 'n', ctrlKey: true }))).toBeNull();
        expect(normalizeKey(keyEvent({ key: 't', ctrlKey: true }))).toBeNull();
    });

    it('accepts the default terminal combo (control+backtick)', () => {
        expect(normalizeKey(keyEvent({ key: '`', ctrlKey: true }))).toBe('control+`');
    });

    it('rejects dead/unknown keys', () => {
        expect(normalizeKey(keyEvent({ key: 'Dead' }))).toBeNull();
        expect(normalizeKey(keyEvent({ key: 'Unidentified' }))).toBeNull();
    });
});

describe('formatShortcut', () => {
    it('uppercases a single key', () => {
        expect(formatShortcut('c')).toBe('C');
        expect(formatShortcut('5')).toBe('5');
    });

    it('formats modifier combos with friendly labels', () => {
        expect(formatShortcut('shift+k')).toBe('Shift+K');
        expect(formatShortcut('control+k')).toBe('Ctrl+K');
        expect(formatShortcut('meta+k')).toBe('Cmd+K');
        expect(formatShortcut('alt+k')).toBe('Alt+K');
        expect(formatShortcut('meta+shift+k')).toBe('Cmd+Shift+K');
    });

    it('formats special keys with readable names', () => {
        expect(formatShortcut('shift+ ')).toBe('Shift+Space');
        expect(formatShortcut('control+f5')).toBe('Ctrl+F5');
    });

    it('formats the default terminal combo', () => {
        expect(formatShortcut('control+`')).toBe('Ctrl+`');
    });
});

describe('parseShortcut', () => {
    it('parses a single key', () => {
        expect(parseShortcut('c')).toEqual({ modifiers: new Set(), key: 'c' });
    });

    it('parses combos', () => {
        const parsed = parseShortcut('meta+shift+k');
        expect(parsed.key).toBe('k');
        expect(parsed.modifiers.has('meta')).toBe(true);
        expect(parsed.modifiers.has('shift')).toBe(true);
        expect(parsed.modifiers.size).toBe(2);
    });
});

describe('matchShortcut', () => {
    it('matches a plain key exactly (no modifiers)', () => {
        expect(matchShortcut('c', keyEvent({ key: 'c' }))).toBe(true);
        expect(matchShortcut('c', keyEvent({ key: 'C', shiftKey: true }))).toBe(false);
        expect(matchShortcut('c', keyEvent({ key: 'd' }))).toBe(false);
    });

    it('matches combos with the exact modifier set', () => {
        expect(matchShortcut('shift+c', keyEvent({ key: 'C', shiftKey: true }))).toBe(true);
        expect(matchShortcut('shift+c', keyEvent({ key: 'c' }))).toBe(false);
        expect(matchShortcut('shift+c', keyEvent({ key: 'C', shiftKey: true, ctrlKey: true }))).toBe(false);
    });

    it('does not confuse control and meta', () => {
        expect(matchShortcut('control+k', keyEvent({ key: 'k', ctrlKey: true }))).toBe(true);
        expect(matchShortcut('control+k', keyEvent({ key: 'k', metaKey: true }))).toBe(false);
    });

    it('never matches an empty (disabled) binding', () => {
        expect(matchShortcut('', keyEvent({ key: 'c' }))).toBe(false);
        expect(matchShortcut('', keyEvent({ key: 'K', shiftKey: true }))).toBe(false);
    });
});

describe('effectiveCreateTaskKey', () => {
    it('falls back to the default "c" when config is missing or unset', () => {
        expect(DEFAULT_SHORTCUTS.create_task).toBe('c');
        expect(effectiveCreateTaskKey(null)).toBe('c');
        expect(effectiveCreateTaskKey(undefined)).toBe('c');
        expect(effectiveCreateTaskKey({} as Config)).toBe('c');
        expect(effectiveCreateTaskKey({ keyboard_shortcuts: {} } as Config)).toBe('c');
    });

    it('returns the custom binding when set', () => {
        expect(effectiveCreateTaskKey({ keyboard_shortcuts: { create_task: 'shift+k' } } as Config)).toBe('shift+k');
    });

    it('returns empty string when explicitly disabled', () => {
        expect(effectiveCreateTaskKey({ keyboard_shortcuts: { create_task: '' } } as Config)).toBe('');
    });
});

describe('effectiveOpenTerminalKey', () => {
    it('defaults to control+backtick when unset', () => {
        expect(effectiveOpenTerminalKey(null)).toBe('control+`');
        expect(effectiveOpenTerminalKey(undefined)).toBe('control+`');
        expect(effectiveOpenTerminalKey({} as Config)).toBe('control+`');
        expect(effectiveOpenTerminalKey({ keyboard_shortcuts: {} } as Config)).toBe('control+`');
    });

    it('returns the stored custom binding', () => {
        expect(effectiveOpenTerminalKey({ keyboard_shortcuts: { open_terminal: 'alt+shift+t' } } as Config)).toBe('alt+shift+t');
    });

    it('returns empty string when disabled', () => {
        expect(effectiveOpenTerminalKey({ keyboard_shortcuts: { open_terminal: '' } } as Config)).toBe('');
    });
});

describe('isMac', () => {
    it('detects Mac platforms from the user agent', () => {
        mockUserAgent(MAC_UA);
        expect(isMac()).toBe(true);
        mockUserAgent('Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X)');
        expect(isMac()).toBe(true);
    });

    it('returns false on other platforms', () => {
        mockUserAgent(WIN_UA);
        expect(isMac()).toBe(false);
        mockUserAgent('Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/120.0');
        expect(isMac()).toBe(false);
    });
});

describe('defaultCreateTaskSubmitKey', () => {
    it('is Cmd+Enter on Mac, Ctrl+Enter elsewhere', () => {
        mockUserAgent(MAC_UA);
        expect(defaultCreateTaskSubmitKey()).toBe('meta+enter');
        mockUserAgent(WIN_UA);
        expect(defaultCreateTaskSubmitKey()).toBe('control+enter');
    });
});

describe('effectiveCreateTaskSubmitKey', () => {
    it('falls back to the platform default when unset', () => {
        mockUserAgent(MAC_UA);
        expect(effectiveCreateTaskSubmitKey(null)).toBe('meta+enter');
        expect(effectiveCreateTaskSubmitKey({} as Config)).toBe('meta+enter');
        expect(effectiveCreateTaskSubmitKey({ keyboard_shortcuts: {} } as Config)).toBe('meta+enter');
        mockUserAgent(WIN_UA);
        expect(effectiveCreateTaskSubmitKey({} as Config)).toBe('control+enter');
    });

    it('returns the custom binding when set', () => {
        expect(effectiveCreateTaskSubmitKey({ keyboard_shortcuts: { create_task_submit: 'shift+enter' } } as Config)).toBe('shift+enter');
    });

    it('returns empty string when explicitly disabled', () => {
        expect(effectiveCreateTaskSubmitKey({ keyboard_shortcuts: { create_task_submit: '' } } as Config)).toBe('');
    });
});

describe('matchesCreateTaskSubmitKey', () => {
    it('while unset, both Ctrl+Enter and Cmd+Enter trigger (any platform)', () => {
        mockUserAgent(WIN_UA);
        expect(matchesCreateTaskSubmitKey({} as Config, keyEvent({ key: 'Enter', ctrlKey: true }))).toBe(true);
        expect(matchesCreateTaskSubmitKey({} as Config, keyEvent({ key: 'Enter', metaKey: true }))).toBe(true);
    });

    it('while unset, plain Enter and other combos never trigger', () => {
        mockUserAgent(MAC_UA);
        expect(matchesCreateTaskSubmitKey({} as Config, keyEvent({ key: 'Enter' }))).toBe(false);
        expect(matchesCreateTaskSubmitKey({} as Config, keyEvent({ key: 'Enter', shiftKey: true }))).toBe(false);
        expect(matchesCreateTaskSubmitKey({} as Config, keyEvent({ key: 'x', ctrlKey: true }))).toBe(false);
        expect(matchesCreateTaskSubmitKey({} as Config, keyEvent({ key: 'Enter', ctrlKey: true, shiftKey: true }))).toBe(false);
    });

    it('an explicit binding matches exactly', () => {
        const cfg = { keyboard_shortcuts: { create_task_submit: 'control+enter' } } as Config;
        expect(matchesCreateTaskSubmitKey(cfg, keyEvent({ key: 'Enter', ctrlKey: true }))).toBe(true);
        expect(matchesCreateTaskSubmitKey(cfg, keyEvent({ key: 'Enter', metaKey: true }))).toBe(false);
        const shifted = { keyboard_shortcuts: { create_task_submit: 'shift+enter' } } as Config;
        expect(matchesCreateTaskSubmitKey(shifted, keyEvent({ key: 'Enter', shiftKey: true }))).toBe(true);
        expect(matchesCreateTaskSubmitKey(shifted, keyEvent({ key: 'Enter', ctrlKey: true }))).toBe(false);
    });

    it('never matches a disabled (empty) binding', () => {
        const cfg = { keyboard_shortcuts: { create_task_submit: '' } } as Config;
        expect(matchesCreateTaskSubmitKey(cfg, keyEvent({ key: 'Enter', ctrlKey: true }))).toBe(false);
        expect(matchesCreateTaskSubmitKey(cfg, keyEvent({ key: 'Enter', metaKey: true }))).toBe(false);
    });
});

describe('defaultStartTaskKey', () => {
    it('is Cmd+Enter on Mac, Ctrl+Enter elsewhere', () => {
        mockUserAgent(MAC_UA);
        expect(defaultStartTaskKey()).toBe('meta+enter');
        mockUserAgent(WIN_UA);
        expect(defaultStartTaskKey()).toBe('control+enter');
    });
});

describe('effectiveStartTaskKey', () => {
    it('falls back to the platform default when unset', () => {
        mockUserAgent(MAC_UA);
        expect(effectiveStartTaskKey(null)).toBe('meta+enter');
        expect(effectiveStartTaskKey(undefined)).toBe('meta+enter');
        expect(effectiveStartTaskKey({} as Config)).toBe('meta+enter');
        expect(effectiveStartTaskKey({ keyboard_shortcuts: {} } as Config)).toBe('meta+enter');
        mockUserAgent(WIN_UA);
        expect(effectiveStartTaskKey({} as Config)).toBe('control+enter');
    });

    it('returns the custom binding when set', () => {
        expect(effectiveStartTaskKey({ keyboard_shortcuts: { start_task: 'shift+enter' } } as Config)).toBe('shift+enter');
    });

    it('returns empty string when explicitly disabled', () => {
        expect(effectiveStartTaskKey({ keyboard_shortcuts: { start_task: '' } } as Config)).toBe('');
    });
});

describe('effectiveToggleFullscreenKey', () => {
    it('defaults to Ctrl+Up when unset (any platform)', () => {
        mockUserAgent(MAC_UA);
        expect(effectiveToggleFullscreenKey(null)).toBe('control+arrowup');
        expect(effectiveToggleFullscreenKey(undefined)).toBe('control+arrowup');
        expect(effectiveToggleFullscreenKey({} as Config)).toBe('control+arrowup');
        expect(effectiveToggleFullscreenKey({ keyboard_shortcuts: {} } as Config)).toBe('control+arrowup');
        mockUserAgent(WIN_UA);
        expect(effectiveToggleFullscreenKey({} as Config)).toBe('control+arrowup');
    });

    it('returns the custom binding when set', () => {
        expect(effectiveToggleFullscreenKey({ keyboard_shortcuts: { toggle_fullscreen: 'shift+alt+f' } } as Config)).toBe('shift+alt+f');
    });

    it('returns empty string when explicitly disabled', () => {
        expect(effectiveToggleFullscreenKey({ keyboard_shortcuts: { toggle_fullscreen: '' } } as Config)).toBe('');
    });
});

describe('fullscreenToggleTitle', () => {
    it('shows the default binding', () => {
        expect(fullscreenToggleTitle(null)).toBe('Toggle fullscreen (Ctrl+Up)');
        expect(fullscreenToggleTitle({} as Config)).toBe('Toggle fullscreen (Ctrl+Up)');
    });

    it('shows a custom binding formatted', () => {
        expect(fullscreenToggleTitle({ keyboard_shortcuts: { toggle_fullscreen: 'shift+alt+f' } } as Config)).toBe('Toggle fullscreen (Shift+Alt+F)');
    });

    it('shows plain text when disabled', () => {
        expect(fullscreenToggleTitle({ keyboard_shortcuts: { toggle_fullscreen: '' } } as Config)).toBe('Toggle fullscreen');
    });
});

describe('shortcutHintsHidden', () => {
    it('defaults to visible (false) when config is missing or the preference is unset', () => {
        expect(shortcutHintsHidden(null)).toBe(false);
        expect(shortcutHintsHidden(undefined)).toBe(false);
        expect(shortcutHintsHidden({} as Config)).toBe(false);
        expect(shortcutHintsHidden({ keyboard_shortcuts: {} } as Config)).toBe(false);
    });

    it('follows the explicit preference', () => {
        expect(shortcutHintsHidden({ keyboard_shortcuts: { hide_hints: true } } as Config)).toBe(true);
        expect(shortcutHintsHidden({ keyboard_shortcuts: { hide_hints: false } } as Config)).toBe(false);
    });
});
