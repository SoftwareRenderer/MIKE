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
    normalizeClick,
    matchesClickBinding,
    isBrowserLinkClick,
    defaultMultiSelectClick,
    effectiveMultiSelectClick,
    effectiveToggleFullscreenKey,
    fullscreenToggleTitle,
    openTerminalTitle,
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

function clickEvent(init: Partial<MouseEventInit> & { button?: number } = {}): MouseEvent {
    return new MouseEvent('click', { cancelable: true, button: 0, ...init });
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

    it('formats click bindings with a readable mouse label', () => {
        expect(formatShortcut('meta+click')).toBe('Cmd+Click');
        expect(formatShortcut('control+click')).toBe('Ctrl+Click');
        expect(formatShortcut('control+shift+click')).toBe('Ctrl+Shift+Click');
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

/**
 * Every binding is stored under one key of `keyboard_shortcuts` and read back by one accessor, and every
 * accessor owes the same three answers: nothing stored is the default, something stored is that binding,
 * and the empty string is a person's decision to disable it. A row here is one binding — an accessor that
 * gets any of the three wrong is a shortcut that silently stops working, or one nobody can switch off.
 */
const BINDINGS: Array<{
    name: string;
    accessor: (config: Config | null | undefined) => string;
    configKey: string;
    /** What the accessor answers when nothing is stored, per platform. */
    fallback: { mac: string; other: string };
    /** The binding the shipped table carries, for the rows it carries one. */
    shipped?: string;
    custom: string;
}> = [
    { name: 'create_task', accessor: effectiveCreateTaskKey, configKey: 'create_task', fallback: { mac: 'c', other: 'c' }, shipped: 'c', custom: 'shift+k' },
    { name: 'open_terminal', accessor: effectiveOpenTerminalKey, configKey: 'open_terminal', fallback: { mac: 'control+`', other: 'control+`' }, shipped: 'control+`', custom: 'alt+shift+t' },
    { name: 'toggle_fullscreen', accessor: effectiveToggleFullscreenKey, configKey: 'toggle_fullscreen', fallback: { mac: 'control+arrowup', other: 'control+arrowup' }, shipped: 'control+arrowup', custom: 'shift+alt+f' },
    { name: 'create_task_submit', accessor: effectiveCreateTaskSubmitKey, configKey: 'create_task_submit', fallback: { mac: 'meta+enter', other: 'control+enter' }, custom: 'shift+enter' },
    { name: 'start_task', accessor: effectiveStartTaskKey, configKey: 'start_task', fallback: { mac: 'meta+enter', other: 'control+enter' }, custom: 'shift+enter' },
    { name: 'multi_select_click', accessor: effectiveMultiSelectClick, configKey: 'multi_select_click', fallback: { mac: 'meta+click', other: 'control+click' }, custom: 'shift+click' },
];

/** A config carrying exactly one stored binding, under exactly one key named at runtime. */
function storedBinding(key: string, value: string): Config {
    return { keyboard_shortcuts: { [key]: value } } as unknown as Config;
}

/** The four shapes of "the config says nothing about this binding" — one decision, spelled four ways. */
const NO_BINDING: Array<[string, Config | null | undefined]> = [
    ['no config at all', null],
    ['an undefined config', undefined],
    ['a config without the shortcuts', {} as Config],
    ['empty shortcuts', { keyboard_shortcuts: {} } as Config],
];

describe.each(BINDINGS)('$name', ({ name, accessor, configKey, fallback, shipped, custom }) => {
    it('falls back to the platform default when nothing is stored', () => {
        for (const [shape, config] of NO_BINDING) {
            mockUserAgent(MAC_UA);
            expect(accessor(config), `on a Mac, ${shape}`).toBe(fallback.mac);
            mockUserAgent(WIN_UA);
            expect(accessor(config), `elsewhere, ${shape}`).toBe(fallback.other);
        }
    });

    it('returns the stored binding, and empty for the binding that is disabled', () => {
        expect(accessor(storedBinding(configKey, custom))).toBe(custom);
        expect(accessor(storedBinding(configKey, ''))).toBe('');
    });

    if (shipped) {
        it('is the binding the shipped table carries', () => {
            expect(DEFAULT_SHORTCUTS[name]).toBe(shipped);
        });
    }
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

describe('normalizeClick', () => {
    it('normalizes modifier clicks in canonical order', () => {
        expect(normalizeClick(clickEvent({ ctrlKey: true }))).toBe('control+click');
        expect(normalizeClick(clickEvent({ metaKey: true }))).toBe('meta+click');
        expect(normalizeClick(clickEvent({ metaKey: true, shiftKey: true }))).toBe('meta+shift+click');
        expect(normalizeClick(clickEvent({ altKey: true, shiftKey: true }))).toBe('shift+alt+click');
    });

    it('rejects an unmodified click — a bare click stays the element\u2019s own action', () => {
        expect(normalizeClick(clickEvent())).toBeNull();
    });

    it('rejects non-primary buttons (middle click opens the browser tab)', () => {
        expect(normalizeClick(clickEvent({ button: 1 }))).toBeNull();
        expect(normalizeClick(clickEvent({ button: 1, ctrlKey: true }))).toBeNull();
        expect(normalizeClick(clickEvent({ button: 2, metaKey: true }))).toBeNull();
    });
});

describe('matchesClickBinding', () => {
    it('matches the exact modifier set', () => {
        expect(matchesClickBinding('meta+click', clickEvent({ metaKey: true }))).toBe(true);
        expect(matchesClickBinding('control+click', clickEvent({ metaKey: true }))).toBe(false);
        expect(matchesClickBinding('control+click', clickEvent())).toBe(false);
    });

    it('does not match when an extra modifier is held', () => {
        expect(matchesClickBinding('control+click', clickEvent({ ctrlKey: true, shiftKey: true }))).toBe(false);
        expect(matchesClickBinding('control+shift+click', clickEvent({ ctrlKey: true, shiftKey: true }))).toBe(true);
    });

    it('ignores non-primary buttons', () => {
        expect(matchesClickBinding('control+click', clickEvent({ ctrlKey: true, button: 1 }))).toBe(false);
    });

    it('never matches a disabled binding or a keyboard binding', () => {
        expect(matchesClickBinding('', clickEvent({ ctrlKey: true }))).toBe(false);
        expect(matchesClickBinding('control+k', clickEvent({ ctrlKey: true }))).toBe(false);
    });
});

describe('isBrowserLinkClick', () => {
    it('leaves unbound modifier clicks to the browser (new tab / new window)', () => {
        const binding = 'control+click';
        expect(isBrowserLinkClick(clickEvent({ metaKey: true }), binding)).toBe(true);
        expect(isBrowserLinkClick(clickEvent({ shiftKey: true }), binding)).toBe(true);
        expect(isBrowserLinkClick(clickEvent({ altKey: true }), binding)).toBe(true);
    });

    it('keeps the bound combo for the app', () => {
        expect(isBrowserLinkClick(clickEvent({ ctrlKey: true }), 'control+click')).toBe(false);
        expect(isBrowserLinkClick(clickEvent({ ctrlKey: true }), '')).toBe(true);
    });

    it('keeps a plain click for the app', () => {
        expect(isBrowserLinkClick(clickEvent(), 'control+click')).toBe(false);
        expect(isBrowserLinkClick(clickEvent(), '')).toBe(false);
    });
});

/** The default a binding carries when the person never touched it is the platform's own key. */
describe.each([
    { name: 'create_task_submit', defaultKey: defaultCreateTaskSubmitKey, mac: 'meta+enter', other: 'control+enter' },
    { name: 'start_task', defaultKey: defaultStartTaskKey, mac: 'meta+enter', other: 'control+enter' },
    { name: 'multi_select_click', defaultKey: defaultMultiSelectClick, mac: 'meta+click', other: 'control+click' },
])('default $name', ({ defaultKey, mac, other }) => {
    it('names the platform\u2019s own key', () => {
        mockUserAgent(MAC_UA);
        expect(defaultKey()).toBe(mac);
        mockUserAgent(WIN_UA);
        expect(defaultKey()).toBe(other);
    });
});

describe.each([
    { title: fullscreenToggleTitle, configKey: 'toggle_fullscreen', label: 'Toggle fullscreen', defaultText: 'Toggle fullscreen (Ctrl+Up)', custom: 'shift+alt+f', customText: 'Toggle fullscreen (Shift+Alt+F)' },
    { title: openTerminalTitle, configKey: 'open_terminal', label: 'Open terminal', defaultText: 'Open terminal (Ctrl+`)', custom: 'alt+shift+t', customText: 'Open terminal (Alt+Shift+T)' },
])('the $label title', ({ title, configKey, defaultText, custom, customText }) => {
    it('names the binding it was given, and names nothing when the binding is disabled', () => {
        mockUserAgent(WIN_UA);
        expect(title(null)).toBe(defaultText);
        expect(title({} as Config)).toBe(defaultText);
        expect(title(storedBinding(configKey, custom))).toBe(customText);
        expect(title(storedBinding(configKey, ''))).toBe(defaultText.replace(/ \(.*\)$/, ''));
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
