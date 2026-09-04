import type { Catalog } from './catalog';
import { en, DEFAULT_LOCALE, type MessageKey } from './en';

export type { Catalog, MessageKey };

/**
 * =============================================================================
 * i18n runtime — where the app's language lives, and the only thing that decides
 * what a message id renders as and what locale formatting runs in.
 * =============================================================================
 *
 * The active language is **derived from the enabled i18n extension**: nothing
 * stores it, there is no `language` setting to keep in sync, and nothing reads a
 * locale out of storage at boot. `setCatalog` is the whole production write
 * surface — the loader (`load-pack.ts`) resolves the enabled pack from the
 * server's extension list, fetches and validates its catalog, and hands it here.
 * `setLocale` is the same write with no catalog behind it: a test/preview seam,
 * in-memory only, so a probe can render a locale without installing a pack. It
 * persists nothing, because a stored language would have to be reconciled with
 * the extension list forever and the two would disagree.
 *
 * `tag` and the catalog table are `$state`, so any component that calls `t()` or
 * `activeLocale()` while rendering re-renders when a pack is enabled or disabled.
 * That is the entire live-switch mechanism: no version bumper, no `$effect`
 * mirror, no reload.
 *
 * Two accessors, deliberately different questions:
 *
 *   • `activeLocale()`  → what to hand `Intl.*` / `toLocale*`. `undefined` means
 *     "no pack, use the browser's" — exactly what every call site did before i18n
 *     existed, so a user with no language pack installed sees no change.
 *   • `documentLanguage()` → what to write into `<html lang>`: the language of the
 *     STRINGS actually rendered, i.e. the active pack's tag else `en`. NOT the
 *     browser's language — without a pack the UI text is English whatever the
 *     browser speaks, and `<html lang>` drives the `:lang()` typography rules (see
 *     the CJK block in `app.css`); claiming Japanese there would restyle English chrome.
 *
 * This file is the only writer of `document.documentElement.lang`, and the only
 * place a locale is ever computed — no consumer may derive one of its own.
 */

/**
 * A message key: a known core key for autocomplete and typo-checking, or any
 * string so an installed pack can address its own namespaced keys.
 */
export type LocaleKey = MessageKey | (string & {});

/** Interpolation parameters for a message: `{name}` tokens in the text. */
export type MessageParams = Record<string, string | number>;

/**
 * `zh`, `fr`, `zh-Hans-CN`… Anything looser is not a tag.
 *
 * Deliberately a **superset** of the rule a pack is installed under:
 * `validateLocale` (`backend/extensions/manifest_validate.go`) holds a pack's declared
 * locale to one bare subtag ("zh", never "zh-CN"), but this runtime also renders tags
 * that predate that rule and any tag `setLocale` is handed in a test. Rejecting them
 * here would be the worse failure — `setCatalog` reads an unparseable tag as "no pack",
 * so a reader with a perfectly good catalog would silently lose their language.
 */
const TAG_RE = /^[A-Za-z]{2,3}(-[A-Za-z0-9]{1,8})*$/;

/** The tag, trimmed and shape-checked, or null. Case is preserved for display. */
function normalizeTag(tag: string | null | undefined): string | null {
    const trimmed = typeof tag === 'string' ? tag.trim() : '';
    if (!trimmed) return null;
    return TAG_RE.test(trimmed) ? trimmed : null;
}

type LocaleListener = (locale: string | undefined) => void;

function interpolate(template: string, params?: MessageParams): string {
    if (!params) return template;
    // Unknown tokens are left alone — a missing parameter should read as the
    // template, not as an empty string hiding a bug.
    return template.replace(/\{(\w+)\}/g, (whole, name: string) =>
        Object.prototype.hasOwnProperty.call(params, name) ? String(params[name]) : whole,
    );
}

export class I18nRuntime {
    /**
     * The tag of the language on screen, or `null` when nothing has chosen one.
     * Reactive: every accessor below reads it, so a write here re-renders the app.
     */
    tag = $state<string | null>(null);

    /**
     * The catalog answering right now — the active tag's own (narrowed by subtag),
     * or null when no tag is set. Exposed for introspection and tests; string
     * lookup goes through `catalogInUse()` so a re-registered pack is read live.
     */
    catalog = $derived(this.packCatalog() ?? null);

    /** The locale the UI is rendering: the active pack's tag, or the fallback. */
    locale = $derived(this.tag ?? DEFAULT_LOCALE);

    /**
     * Catalogs installed on this runtime, keyed by lower-cased tag: `en` is the
     * catalog of record and always present, packs arrive later through
     * `registerCatalog` (a language pack is fetched long after this module was
     * imported, so the table cannot be a static import).
     */
    private table = new Map<string, Catalog>([[DEFAULT_LOCALE, en]]);

    /** Listeners notified when the tag changes — cached-state consumers only. */
    private listeners = new Set<LocaleListener>();

    /**
     * Keys already warned about for the current pack. A missing key is a defect in
     * the pack, reported once rather than on every render — and reset when the tag
     * changes, because a reloaded pack may have fixed it.
     */
    private warned = new Set<string>();

    /**
     * Install (or replace) a catalog under a tag. Re-registering replaces, which is
     * how an update to a pack takes effect.
     *
     * Returns false for a malformed tag, so a bad manifest cannot install a catalog
     * that nothing could ever select. The `en` slot is core's own and not writable
     * from here: a pack declaring `en` must not shadow the catalog of record.
     */
    registerCatalog(tag: string, catalog: Catalog): boolean {
        const normalized = normalizeTag(tag);
        if (!normalized || normalized.toLowerCase() === DEFAULT_LOCALE) return false;
        // Snapshot: from this point the table holds a fixed set of strings, so a
        // loader mutating its working object cannot change the UI one key at a time.
        this.table.set(normalized.toLowerCase(), { ...catalog });
        return true;
    }

    /**
     * The pack answering for `tag`, narrowed by subtag (`zh-Hans-CN` → `zh-Hans` →
     * `zh`), or undefined when no pack is installed for it — which is NOT `en`:
     * `packText()` relies on that distinction to let `Intl` answer in the chosen
     * locale rather than in English.
     */
    packCatalog(tag: string = this.tag ?? ''): Catalog | undefined {
        if (!tag) return undefined;
        const parts = tag.toLowerCase().split('-');
        while (parts.length > 0) {
            const found = this.table.get(parts.join('-'));
            if (found && found !== en) return found;
            parts.pop();
        }
        return undefined;
    }

    /**
     * Install (or remove) the active pack's catalog — the production write surface.
     *
     * `messages` is a finished, validated object: one assignment plus one tag
     * write, so the UI can never observe a half-loaded pack. Passing `null` returns
     * to English, and is what a missing, disabled, or failed-to-load pack looks
     * like from here.
     */
    setCatalog(locale: string, messages: Catalog | null): void {
        const tag = normalizeTag(locale);
        const pack =
            messages && tag && tag.toLowerCase() !== DEFAULT_LOCALE ? this.accepted(tag, messages) : null;
        this.apply(pack ? tag : null, pack !== null);
    }

    /**
     * Choose the active tag with no catalog of its own (test/preview only — the
     * app's language comes from `setCatalog`). `null` clears the choice, which
     * restores the pre-i18n behaviour everywhere: `Intl` follows the browser and
     * `<html lang>` says `en`.
     *
     * A malformed tag does nothing at all, so a bad value cannot silently drop the
     * user out of their language. Nothing is persisted: see the header.
     */
    setLocale(tag: string | null): void {
        if (tag !== null && normalizeTag(tag) === null) return;
        this.apply(normalizeTag(tag), false);
    }

    /**
     * Resolve a message id: `pack → en → the id itself`.
     *
     * The last step is the only way a missing key can reach the screen, and it is
     * always a bug in a pack or in this file. When a pack is active and cannot
     * answer, the English text is used and the miss is warned once, so a partially
     * translated pack degrades per key instead of producing a UI of raw ids or a
     * half-English wall.
     */
    t(key: LocaleKey, params?: MessageParams): string {
        const pack = this.packCatalog();
        let text = pack?.[key];
        if (text === undefined) {
            // Only warn while a pack is in use: with no pack this is English, and
            // an id missing from `en` is the conversion task's to-do list, not a
            // runtime fault.
            if (pack && !this.warned.has(key)) {
                this.warned.add(key);
                const fallback = en[key];
                console.warn(
                    `[i18n] ${this.locale} catalog has no entry for ${JSON.stringify(key)}; ` +
                        `falling back to ${fallback === undefined ? 'the message id' : 'English'}`,
                );
            }
            text = en[key];
        }
        if (text === undefined) return key;
        return params ? interpolate(text, params) : text;
    }

    /**
     * The value of `key` in the catalog in use, or `undefined` when that catalog
     * does not ship it — for callers with a better answer than English (the relative
     * time rungs hand the job to `Intl.RelativeTimeFormat` instead).
     */
    packText(key: LocaleKey, params?: MessageParams): string | undefined {
        const value = this.catalogInUse()?.[key];
        return value === undefined ? undefined : interpolate(value, params);
    }

    /** Whether the PACK in use answers `key` — English does not count here. */
    has(key: LocaleKey): boolean {
        return this.packCatalog()?.[key] !== undefined;
    }

    /** Whether the catalog in use answers `key` (English counts; badges, "is this translated?"). */
    hasKey(key: LocaleKey): boolean {
        return this.catalogInUse()?.[key] !== undefined;
    }

    /** The locale to pass to `Intl.*` — `undefined` means "ask the browser". */
    activeLocale(): string | undefined {
        return this.tag ?? undefined;
    }

    /** The concrete tag `Intl` will end up using (for debug output and probes). */
    resolvedLocale(): string {
        if (this.tag) return this.tag;
        const browser = typeof navigator === 'undefined' ? '' : navigator.language;
        return normalizeTag(browser) ?? DEFAULT_LOCALE;
    }

    /** The tag describing the rendered strings — what `<html lang>` must say. */
    documentLanguage(): string {
        return this.locale;
    }

    /**
     * Subscribe to tag changes. Reactive callers do NOT need this (they read
     * `activeLocale()`/`t()` and re-run on their own); it exists for imperative
     * consumers holding cached state — a canvas redraw, a formatter memo, a
     * CodeMirror reconfigure. Returns the unsubscribe function.
     */
    onLocaleChange(listener: LocaleListener): () => void {
        this.listeners.add(listener);
        return () => {
            this.listeners.delete(listener);
        };
    }

    /**
     * The catalog `packText`/`hasKey` read. With no tag set this is the `en` catalog
     * of record; with a tag chosen it is that tag's own pack, **or nothing** — not
     * English. The gap is the point: a chosen locale with no pack installed answers
     * nothing, so `Intl` (which does know the chosen tag) gets the job instead of an
     * English phrase dressed up in the wrong language. `t()` deliberately does not
     * stop here — it keeps the English fallback, because a caller with no better
     * answer must still render something readable.
     */
    private catalogInUse(): Catalog | undefined {
        return this.tag === null ? en : this.packCatalog();
    }

    /** A pack is only installed once it is not the fallback locale. */
    private accepted(tag: string, messages: Catalog): Catalog | null {
        return this.registerCatalog(tag, messages) ? this.table.get(tag.toLowerCase())! : null;
    }

    /**
     * The one place the tag moves: mirror it onto the document and tell the
     * imperative listeners. `clearWarned` is false for a same-tag re-register, so
     * replacing a catalog under the tag in use does not re-print every miss.
     */
    private apply(next: string | null, clearWarned: boolean): void {
        const changed = next !== this.tag;
        this.tag = next;
        if (changed || clearWarned) this.warned.clear();
        this.applyDocumentLanguage();
        if (!changed) return;
        // One listener throwing must not stop the others (or the switch itself).
        for (const listener of [...this.listeners]) {
            try {
                listener(this.tag ?? undefined);
            } catch (err) {
                console.error('[i18n] locale change listener threw:', err);
            }
        }
    }

    /** Mirror the active locale onto the document, which owns it from here. */
    private applyDocumentLanguage(): void {
        if (typeof document === 'undefined') return;
        document.documentElement.lang = this.locale;
    }
}

/** The app-wide runtime. */
export const i18n = new I18nRuntime();

/**
 * Translate a message id against the active pack (falling back to English, then to
 * the id). Reads the runtime's `$state`, so a component calling this re-renders
 * when the enabled pack changes.
 */
export function t(key: LocaleKey, params?: MessageParams): string {
    return i18n.t(key, params);
}

/** The value of `key` in the catalog in use, or `undefined` when it is not there. */
export function packText(key: LocaleKey, params?: MessageParams): string | undefined {
    return i18n.packText(key, params);
}

/** Whether the PACK in use answers `key` (English does not count). */
export function has(key: LocaleKey): boolean {
    return i18n.has(key);
}

/** Whether the catalog in use answers `key`, English included. */
export function hasKey(key: LocaleKey): boolean {
    return i18n.hasKey(key);
}

/** The locale the UI renders in for `Intl.*` — the pack's tag, else `undefined`. */
export function activeLocale(): string | undefined {
    return i18n.activeLocale();
}

/** The concrete tag `Intl` ends up using, browser language included. */
export function resolvedLocale(): string {
    return i18n.resolvedLocale();
}

/** The tag describing the rendered strings — the `<html lang>` value. */
export function documentLanguage(): string {
    return i18n.documentLanguage();
}

/** Subscribe to locale changes; returns the unsubscribe function. */
export function onLocaleChange(listener: LocaleListener): () => void {
    return i18n.onLocaleChange(listener);
}

/** Install a catalog at runtime (T5's entry point when a pack is fetched). */
export function registerCatalog(tag: string, catalog: Catalog): boolean {
    return i18n.registerCatalog(tag, catalog);
}

/**
 * Choose the active tag with no catalog of its own — **test and preview seam**.
 * Production language always arrives through `i18n.setCatalog` from the enabled
 * pack; nothing here is persisted.
 */
export function setLocale(tag: string | null): void {
    i18n.setLocale(tag);
}
