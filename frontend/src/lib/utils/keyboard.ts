/**
 * Keyboard shortcut utilities.
 * Normalized form: modifiers in the fixed order control, meta, shift, alt
 * joined with "+" then the key (e.g. "c", "shift+k", "control+k").
 * Binding semantics: undefined = default, "" = disabled, non-empty = custom.
 */
import type { Config } from '$lib/types';

/** Default keyboard shortcut bindings. */
export const DEFAULT_SHORTCUTS: Record<string, string> = {
    create_task: 'c',
};

const MODIFIERS = ['control', 'meta', 'shift', 'alt'] as const;
type Modifier = (typeof MODIFIERS)[number];

/** Browser-reserved combos that must not be rebindable. */
const RESERVED_COMBOS = new Set(['control+w', 'control+q', 'control+n', 'control+t']);

const MODIFIER_LABELS: Record<Modifier, string> = {
    control: 'Ctrl',
    meta: 'Cmd',
    shift: 'Shift',
    alt: 'Alt',
};

/** Modifier key names — pressing one alone is not a binding. */
const MODIFIER_KEYS = new Set([
    'control', 'meta', 'shift', 'alt', 'os', 'hyper', 'super', 'fn', 'fnlock',
    'capslock', 'numlock', 'scrolllock',
]);

const KEY_LABELS: Record<string, string> = {
    ' ': 'Space',
    enter: 'Enter',
    tab: 'Tab',
    backspace: 'Backspace',
    delete: 'Delete',
    arrowup: 'Up',
    arrowdown: 'Down',
    arrowleft: 'Left',
    arrowright: 'Right',
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

function buildCombo(e: KeyboardEvent, key: string): string {
    const parts: string[] = [];
    if (e.ctrlKey) parts.push('control');
    if (e.metaKey) parts.push('meta');
    if (e.shiftKey) parts.push('shift');
    if (e.altKey) parts.push('alt');
    return [...parts, key].join('+');
}

/** Format a normalized shortcut for display: `c` → `C`, `shift+k` → `Shift+K`. */
export function formatShortcut(key: string): string {
    const parts = key.split('+');
    const keyPart = parts.pop() ?? key;
    const labels = parts.map(m => MODIFIER_LABELS[m as Modifier] ?? m);
    labels.push(KEY_LABELS[keyPart] ?? keyPart.toUpperCase());
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
    const actual = new Set<string>();
    if (e.ctrlKey) actual.add('control');
    if (e.metaKey) actual.add('meta');
    if (e.shiftKey) actual.add('shift');
    if (e.altKey) actual.add('alt');
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
