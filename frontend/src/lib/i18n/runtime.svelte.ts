import type { Catalog } from './catalog';
import { SvelteMap } from 'svelte/reactivity';
import {
    en,
    DEFAULT_LOCALE,
    type MessageKey,
    type MessageParams,
    type MessageValue,
    type PluralTable,
} from './en';

export type { Catalog, MessageKey, MessageParams, MessageValue };

/**
 * The i18n runtime: the only writer of the active locale and of
 * `document.documentElement.lang`. The language is derived from the enabled i18n
 * extension — nothing stores it, so there is no setting to reconcile with the
 * extension list. `setCatalog` is the production write surface; `setLocale` is the
 * in-memory test/preview seam.
 *
 * Two different questions, deliberately separate answers:
 *   - `activeLocale()`   → what to hand `Intl.*`; undefined means "use the browser's".
 *   - `documentLanguage()` → what to write into `<html lang>`: the language of the
 *     strings actually rendered (the pack's tag, else `en`), never the browser's —
 *     that tag drives the `:lang()` typography rules in app.css.
 *
 * Fallback is per key, never per catalog: pack value, then `en.ts` with one warning,
 * then the key itself with a warning (a visible key id is a bug report; an empty
 * string is a mystery). Full mechanism: docs/i18n.md.
 */

/**
 * A message key a *pack-facing* accessor accepts: a known core key for autocomplete
 * and typo-checking, or any string, because `packText`/`has` are asked about keys a
 * pack authors itself (`time.${unit}Ago` built from a unit name).
 *
 * `t()` deliberately does **not** take this — it takes `MessageKey`, so a typo in a
 * core key is a build error rather than an id on screen. Widening it there would
 * cost the one check that keeps 1100 keys honest, and no core call site needs it.
 */
export type LocaleKey = MessageKey | (string & {});

/**
 * `de`, `pt-BR`, `sr-Latn-RS`… Anything looser is not a tag.
 *
 * Deliberately a **superset** of the rule a pack is installed under:
 * `validateLocale` (`backend/extensions/manifest_validate.go`) holds a pack's declared
 * locale to one bare subtag ("de", never "de-DE"), but this runtime also renders tags
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

/**
 * Fallbacks are a development signal. `import.meta.env.PROD` is a build-time
 * constant, so a production bundle folds these warnings away (verified: the message
 * strings do not survive `npm run build`), while `vite dev` and vitest keep them
 * live — the test suite asserts on them precisely so a fallback cannot go quiet.
 */
const DEV = !import.meta.env.PROD;

const pluralRules = new Map<string, Intl.PluralRules>();
function rulesFor(code: string): Intl.PluralRules {
    let rules = pluralRules.get(code);
    if (!rules) {
        rules = new Intl.PluralRules(code);
        pluralRules.set(code, rules);
    }
    return rules;
}

function interpolate(template: string, params?: MessageParams): string {
    if (!params) return template;
    // Unknown tokens are left alone — a missing parameter should read as the
    // template, not as an empty string hiding a bug. One pass, so a substituted
    // value can never inject a second placeholder.
    return template.replace(/\{(\w+)\}/g, (whole, name: string) =>
        Object.prototype.hasOwnProperty.call(params, name) && params[name] !== undefined
            ? String(params[name])
            : whole,
    );
}

/** @knipignore A runtime built by a test, so its state and warnings cannot reach the app-wide one. */
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
         * Catalogs installed on this runtime, keyed by lower-cased tag. `en` is the catalog of
         * record; packs arrive later through `registerCatalog`.
         *
         * A reactive Map, not a plain one: `boot.ts` applies the cached catalog under a tag and
         * `load-pack.ts` then hands the same tag the freshly fetched bytes. A plain Map makes
         * that second write invisible to the UI, which keeps showing a stale catalog until the
         * locale itself moves.
         */
    private table = new SvelteMap<string, Catalog>([[DEFAULT_LOCALE, en]]);

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
        // Snapshot: from this point the table holds a fixed set of values, so a
        // loader mutating its working object cannot change the UI one key at a time.
        this.table.set(normalized.toLowerCase(), { ...catalog });
        return true;
    }

    /**
     * The pack answering for `tag`, narrowed by subtag (`sr-Latn-RS` → `sr-Latn` →
     * `sr`), or undefined when no pack is installed for it — which is NOT `en`:
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
     * Install (or remove) the active pack's catalog — the production write surface,
     * and the live swap: calling it again with a new catalog for the tag in use
     * re-renders everything that tag has translated, and everything it has not
     * still reads English.
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
        const fromPack = pack?.[key];
        if (fromPack !== undefined) return this.render(fromPack, params);

        const english = en[key];
        if (english === undefined) {
            // Reported whether or not a pack is active: with `t()` typed to
            // `MessageKey`, an id that reaches here came from a cast or a pack
            // template, and both are bugs. Silent is the one wrong answer — a
            // fallback nobody sees is a fallback that never gets fixed.
            this.warnOnce(
                key,
                `[i18n] no catalog has the message key ${JSON.stringify(key)} — ` +
                    `rendering the message id so the hole is visible`,
            );
            return key;
        }
        if (pack) {
            this.warnOnce(
                key,
                `[i18n] ${this.locale} catalog has no entry for ${JSON.stringify(key)}; ` +
                    `falling back to English`,
            );
        }
        return this.render(english, params);
    }

    /**
     * The value of `key` in the catalog in use, or `undefined` when that catalog
     * does not ship it — for callers with a better answer than English (the relative
     * time rungs hand the job to `Intl.RelativeTimeFormat` instead).
     */
    packText(key: LocaleKey, params?: MessageParams): string | undefined {
        const value = this.catalogInUse()?.[key];
        return value === undefined ? undefined : this.render(value, params);
    }

    /**
     * Whether the PACK in use answers `key` — English does not count here.
     * `packText`/`hasKey` read the catalog in use (English included); this answers
     * only for what the enabled pack itself translated.
     */
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
     * Write `<html lang>`. Public because module load has to call it once — the
     * attribute is this module's from the first frame, not from the first switch,
     * and nothing outside this file may write it.
     */
    syncDocumentLanguage(): void {
        if (typeof document === 'undefined') return;
        const lang = this.locale;
        if (document.documentElement.lang !== lang) document.documentElement.lang = lang;
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
     * A message value into text: a plural table selects its form through
     * `Intl.PluralRules` for the locale on screen (never `count === 1`, which is
     * right only in English), then named `{tokens}` are interpolated.
     *
     * A table that does not declare the category the locale asks for degrades to
     * `other` — which is also the whole form for a single-form language, so a pack
     * may ship `other` alone.
     */
    private render(value: MessageValue, params?: MessageParams): string {
        if (typeof value === 'string') return interpolate(value, params);
        const table = value as PluralTable;
        const count = params?.['count'];
        const category = typeof count === 'number' ? rulesFor(this.locale).select(count) : 'other';
        return interpolate(table[category] ?? table.other, params);
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
        this.syncDocumentLanguage();
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

    private warnOnce(key: LocaleKey, message: string): void {
        if (!DEV || this.warned.has(key)) return;
        this.warned.add(key);
        console.warn(message);
    }
}

type LocaleListener = (locale: string | undefined) => void;

/** The app-wide runtime. */
export const i18n = new I18nRuntime();

/**
 * The tag the document was painted with, if it is not the fallback. Only the pre-paint
 * script in `index.html` can put a non-`en` tag here before this module runs. Adopting it
 * is what makes that script real: syncing `en` over it undid the hint one frame later and
 * a reload flashed back through English between two Chinese frames.
 *
 * The document is the only input, so a first run still starts in English.
 */
function prePaintTag(): string | null {
    if (typeof document === 'undefined') return null;
    const tag = normalizeTag(document.documentElement.lang);
    return tag && tag.toLowerCase() !== DEFAULT_LOCALE ? tag : null;
}

// `<html lang>` is this module's from the first frame, before any pack resolves.
const paintedTag = prePaintTag();
if (paintedTag) i18n.setLocale(paintedTag);
else i18n.syncDocumentLanguage();

/**
 * Translate a message id against the active pack (falling back to English, then to
 * the id). Reads the runtime's `$state`, so a component calling this re-renders
 * when the enabled pack changes.
 *
 * `MessageKey`-typed on purpose: every string the UI renders is a core key, and a
 * typo must be a `svelte-check` error, not text on screen.
 */
export function t(key: MessageKey, params?: MessageParams): string {
    return i18n.t(key, params);
}

/** The locale the interface is rendering in — the pack's tag, else `en`. */
export const locale = {
    get code(): string {
        return i18n.locale;
    },
};

/** Install a catalog and activate its tag; `null` messages return to English. */
export function setCatalog(code: string, messages: Catalog | null): void {
    i18n.setCatalog(code, messages);
}

/** Drop back to the English catalog of record — a pack disabled or uninstalled. */
export function resetToEnglish(): void {
    i18n.setCatalog(DEFAULT_LOCALE, null);
}

/** The value of `key` in the catalog in use, or `undefined` when it is not there. */
export function packText(key: LocaleKey, params?: MessageParams): string | undefined {
    return i18n.packText(key, params);
}

/**
 * Whether the catalog in use answers `key`, English included.
 *
 * @knipignore Which keys the installed catalog answers, probed with one it has and one it has not.
 */
export function hasKey(key: LocaleKey): boolean {
    return i18n.hasKey(key);
}

/** The locale to pass to `Intl.*` — the pack's tag, else `undefined`. */
export function activeLocale(): string | undefined {
    return i18n.activeLocale();
}

/**
 * The concrete tag `Intl` ends up using, browser language included.
 *
 * @knipignore The tag `Intl` ends up with, browser default included.
 */
export function resolvedLocale(): string {
    return i18n.resolvedLocale();
}

/**
 * The tag describing the rendered strings — the `<html lang>` value.
 *
 * @knipignore The tag `<html lang>` carries, which is not the tag `Intl` gets.
 */
export function documentLanguage(): string {
    return i18n.documentLanguage();
}

/**
 * Subscribe to locale changes; returns the unsubscribe function.
 *
 * @knipignore Which locale a subscriber is told about, and that unsubscribing stops the telling.
 */
export function onLocaleChange(listener: LocaleListener): () => void {
    return i18n.onLocaleChange(listener);
}

/**
 * Install a catalog at runtime (T5's entry point when a pack is fetched).
 *
 * @knipignore How the loading and formatting tests install a pack to run under.
 */
export function registerCatalog(tag: string, catalog: Catalog): boolean {
    return i18n.registerCatalog(tag, catalog);
}

/**
 * Choose the active tag with no catalog of its own — **test and preview seam**.
 * Production language always arrives through `i18n.setCatalog` from the enabled
 * pack; nothing here is persisted.
 *
 * @knipignore Picks the tag a test runs under — every formatting test installs its locale here.
 */
export function setLocale(tag: string | null): void {
    i18n.setLocale(tag);
}
