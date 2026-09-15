/**
 * Keyboard shortcut utilities.
 * Normalized form: modifiers in the fixed order control, meta, shift, alt
 * joined with "+" then the key (e.g. "c", "shift+k", "control+k"). A key may
 * also be the pseudo-key "click" for mouse bindings (e.g. "meta+click").
 * Binding semantics: undefined = default, "" = disabled, non-empty = custom.
 */
import type { Config } from '$lib/types';
import { t, type MessageKey } from '$lib/i18n';

/** Default keyboard shortcut bindings. */
export const DEFAULT_SHORTCUTS: Record<string, string> = {
    create_task: 'c',
    // Ctrl+backtick — classic terminal shortcut (VS Code, iTerm); not reserved
    // by browsers on any platform (Cmd+backtick is macOS window-cycling, so
    // the default always uses control, not meta).
    'open_terminal': 'control+`',
    // Ctrl+Up — maximize/minimize the focused area (chat, diff, editor, terminal).
    'toggle_fullscreen': 'control+arrowup',
};

const MODIFIERS = ['control', 'meta', 'shift', 'alt'] as const;
type Modifier = (typeof MODIFIERS)[number];

/** Browser-reserved combos that must not be rebindable. */
const RESERVED_COMBOS = new Set(['control+w', 'control+q', 'control+n', 'control+t']);

/**
 * The legend a keycap hint shows for a modifier. Catalog keys, never legends —
 * a key name is copy like any other ('Entrée', 'Rücktaste'), and the binding
 * itself stays the normalized `control+k` form no matter the locale.
 */
const MODIFIER_LABEL_KEYS: Record<Modifier, MessageKey> = {
    control: 'app.key.control',
    meta: 'app.key.meta',
    shift: 'app.key.shift',
    alt: 'app.key.alt',
};

/** Modifier key names — pressing one alone is not a binding. */
const MODIFIER_KEYS = new Set([
    'control', 'meta', 'shift', 'alt', 'os', 'hyper', 'super', 'fn', 'fnlock',
    'capslock', 'numlock', 'scrolllock',
]);

/** Pseudo-key used by mouse bindings (e.g. `meta+click` = Cmd+Click). */
const CLICK_KEY = 'click';

/** Legend per named key; anything unlisted is displayed uppercased. */
const KEY_LABEL_KEYS: Record<string, MessageKey> = {
    ' ': 'app.key.space',
    [CLICK_KEY]: 'app.key.click',
    enter: 'app.key.enter',
    tab: 'app.key.tab',
    backspace: 'app.key.backspace',
    delete: 'app.key.delete',
    arrowup: 'app.key.up',
    arrowdown: 'app.key.down',
    arrowleft: 'app.key.left',
    arrowright: 'app.key.right',
};

/** Convert a keydown event to a normalized shortcut string, or null if not a valid binding. */
export function normalizeKey(e: KeyboardEvent): string | null {
    const { key } = e;
    if (key === 'Escape' || key === 'Dead' || key === 'Unidentified') return null;
    if (MODIFIER_KEYS.has(key.toLowerCase())) return null;
    const hasModifier = e.ctrlKey || e.metaKey || e.shiftKey || e.altKey;
    if (key.length !== 1 && !hasModifier) return null;
    // Space behaves like a named key — require a modifier.
    if (key === ' ' && !hasModifier) return null;
    const combo = buildCombo(e, key.toLowerCase());
    return RESERVED_COMBOS.has(combo) ? null : combo;
}

/** Modifier names held on a keyboard or mouse event, in canonical order. */
function modifierParts(e: MouseEvent | KeyboardEvent): string[] {
    const parts: string[] = [];
    if (e.ctrlKey) parts.push('control');
    if (e.metaKey) parts.push('meta');
    if (e.shiftKey) parts.push('shift');
    if (e.altKey) parts.push('alt');
    return parts;
}

function buildCombo(e: KeyboardEvent, key: string): string {
    return [...modifierParts(e), key].join('+');
}

/** Format a normalized shortcut for display: `c` → `C`, `shift+k` → `Shift+K`. */
export function formatShortcut(key: string): string {
    const parts = key.split('+');
    const keyPart = parts.pop() ?? key;
    const labels = parts.map(m => {
        const labelKey: MessageKey | undefined = MODIFIER_LABEL_KEYS[m as Modifier];
        return labelKey ? t(labelKey) : m;
    });
    const keyLabel: MessageKey | undefined = KEY_LABEL_KEYS[keyPart];
    labels.push(keyLabel ? t(keyLabel) : keyPart.toUpperCase());
    return labels.join('+');
}

/** Parse a normalized shortcut into its modifier set and key. */
export function parseShortcut(key: string): { modifiers: Set<string>; key: string } {
    const parts = key.split('+');
    const keyPart = parts.pop() ?? key;
    return { modifiers: new Set(parts), key: keyPart };
}

/** Check whether a KeyboardEvent matches a normalized shortcut (exact modifiers + key). */
export function matchShortcut(key: string, e: KeyboardEvent): boolean {
    if (!key) return false;
    const { modifiers, key: expectedKey } = parseShortcut(key);
    const actual = new Set<string>(modifierParts(e));
    if (expectedKey !== e.key.toLowerCase()) return false;
    if (modifiers.size !== actual.size) return false;
    for (const m of modifiers) {
        if (!actual.has(m)) return false;
    }
    return true;
}

/** Resolve the effective create-task shortcut: the default when unset, `""` when disabled. */
export function effectiveCreateTaskKey(config: Config | null | undefined): string {
    return config?.keyboard_shortcuts?.create_task ?? DEFAULT_SHORTCUTS.create_task;
}

/** Resolve the effective open-terminal shortcut: the default when unset, `""` when disabled. */
export function effectiveOpenTerminalKey(config: Config | null | undefined): string {
    return config?.keyboard_shortcuts?.open_terminal ?? DEFAULT_SHORTCUTS.open_terminal;
}

/** Platform default for the start-task binding: Cmd+Enter on Mac, Ctrl+Enter elsewhere. */
export function defaultStartTaskKey(): string {
    return isMac() ? 'meta+enter' : 'control+enter';
}

/** Resolve the effective start-task binding: the stored value if set, else the platform default. */
export function effectiveStartTaskKey(config: Config | null | undefined): string {
    return config?.keyboard_shortcuts?.start_task ?? defaultStartTaskKey();
}

/** Convert a mouse event to a normalized click binding (e.g. `meta+click`), or null when it isn't one. */
export function normalizeClick(e: MouseEvent): string | null {
    // Middle/right clicks belong to the browser.
    if (e.button !== 0) return null;
    const parts = modifierParts(e);
    // A bare click is the element's own action, never a binding.
    if (parts.length === 0) return null;
    return [...parts, CLICK_KEY].join('+');
}

/** True when a mouse event matches a click binding exactly (same modifier set, primary button). */
export function matchesClickBinding(binding: string, e: MouseEvent): boolean {
    if (!binding) return false;
    if (e.button !== 0) return false;
    const { modifiers, key } = parseShortcut(binding);
    if (key !== CLICK_KEY) return false;
    const actual = new Set<string>(modifierParts(e));
    if (modifiers.size !== actual.size) return false;
    for (const m of modifiers) {
        if (!actual.has(m)) return false;
    }
    return true;
}

/**
 * True when the browser should keep the click (its link opens in a new tab/window):
 * a modifier is held that the binding doesn't claim. Never `preventDefault()` one.
 */
export function isBrowserLinkClick(e: MouseEvent, binding: string): boolean {
    const hasModifier = e.ctrlKey || e.metaKey || e.shiftKey || e.altKey;
    return hasModifier && !matchesClickBinding(binding, e);
}

/**
 * The click of a link-like button: an unmodified click runs `action` in this document, and a
 * modifier click is left uncancelled so the browser opens the href in a new tab or window.
 */
export function handleLinkClick(e: MouseEvent, action: () => void): void {
    if (e.ctrlKey || e.metaKey || e.shiftKey || e.altKey) return;
    e.preventDefault();
    action();
}

/** The keydown of a link-like button: `role="button"` costs an explicit Space activation. */
export function handleLinkKeydown(e: KeyboardEvent, action: () => void): void {
    if (e.key !== ' ') return;
    e.preventDefault();
    action();
}

/** Platform default for the card multi-select click: Cmd+Click on Mac, Ctrl+Click elsewhere. */
export function defaultMultiSelectClick(): string {
    return isMac() ? 'meta+click' : 'control+click';
}

/** Resolve the effective multi-select click binding: the stored value if set, else the platform default (`""` = disabled). */
export function effectiveMultiSelectClick(config: Config | null | undefined): string {
    return config?.keyboard_shortcuts?.multi_select_click ?? defaultMultiSelectClick();
}

/** Resolve the effective toggle-fullscreen binding: the default when unset, `""` when disabled. */
export function effectiveToggleFullscreenKey(config: Config | null | undefined): string {
    return config?.keyboard_shortcuts?.toggle_fullscreen ?? DEFAULT_SHORTCUTS.toggle_fullscreen;
}

/** Tooltip for the fullscreen-toggle buttons: the binding, or plain text when disabled. */
export function fullscreenToggleTitle(config: Config | null | undefined): string {
    const key = effectiveToggleFullscreenKey(config);
    return key
        ? t('app.shortcut.toggleFullscreenWithKey', { key: formatShortcut(key) })
        : t('app.shortcut.toggleFullscreen');
}

/** Tooltip for the open-terminal button: the binding, or plain text when disabled. */
export function openTerminalTitle(config: Config | null | undefined): string {
    const key = effectiveOpenTerminalKey(config);
    return key
        ? t('app.shortcut.openTerminalWithKey', { key: formatShortcut(key) })
        : t('app.shortcut.openTerminal');
}

/** True on Apple platforms (used to pick the platform-appropriate submit key). */
export function isMac(): boolean {
    if (typeof navigator === 'undefined') return false;
    return /Mac|iPhone|iPad|iPod/i.test(navigator.userAgent);
}

/** Platform default for the create-task submit binding: Cmd+Enter on Mac, Ctrl+Enter elsewhere. */
export function defaultCreateTaskSubmitKey(): string {
    return isMac() ? 'meta+enter' : 'control+enter';
}

/** Resolve the effective submit binding: the stored value if set, else the platform default. */
export function effectiveCreateTaskSubmitKey(config: Config | null | undefined): string {
    return config?.keyboard_shortcuts?.create_task_submit ?? defaultCreateTaskSubmitKey();
}

/**
 * Check whether a keydown matches the create-task submit binding.
 * While unset, both Ctrl+Enter and Cmd+Enter trigger (any platform);
 * an explicit binding matches exactly; `""` never matches.
 */
export function matchesCreateTaskSubmitKey(config: Config | null | undefined, e: KeyboardEvent): boolean {
    const stored = config?.keyboard_shortcuts?.create_task_submit;
    if (stored === undefined) return matchShortcut('control+enter', e) || matchShortcut('meta+enter', e);
    if (!stored) return false;
    return matchShortcut(stored, e);
}

/** True when the user hid the keycap hints (Settings → Preferences). */
export function shortcutHintsHidden(config: Config | null | undefined): boolean {
    return config?.keyboard_shortcuts?.hide_hints === true;
}

/** True when the element receives text input (shortcuts/arrow nav shouldn't fire). */
export function isEditableTarget(el: EventTarget | null): boolean {
    if (!(el instanceof HTMLElement)) return false;
    if (el.isContentEditable) return true;
    const tag = el.tagName;
    return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT';
}
