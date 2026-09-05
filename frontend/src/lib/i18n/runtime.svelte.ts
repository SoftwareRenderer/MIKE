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
 * `tag` and the catalog table are `$state`/`$derived`, so any component that calls
 * `t()` or `activeLocale()` while rendering re-renders when a pack is enabled,
 * replaced or disabled. That is the entire live-switch mechanism: no version
 * bumper, no `$effect` mirror, no reload.
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
 * `locale.code` is the same answer as `documentLanguage()` — the locale the
 * interface is rendering in — and is what the converted components' `t()` calls
 * pair with. It is never a translated language *name*; core names no language.
 *
 * **The fallback ladder, per key and never per catalog.** A pack may be partial:
 *
 * 1. the active pack's value for the key;
 * 2. the `en.ts` value — plus one warning naming the locale and the key, so "the
 *    pack silently never translated this" cannot rot into a half-translated UI;
 * 3. the key itself — plus a warning, because a key nothing has is a hole in the
 *    catalog of record. The key prints rather than nothing on purpose: a visible
 *    `settings.extensions.foo` is a bug report and an empty string is a mystery
 *    (and the a11y test compares a control's accessible name against its rendered
 *    text, so both sides must resolve to the same thing in any catalog state).
 *
 * Warnings are deduped per key and cleared when the tag moves (a reloaded pack may
 * have fixed it), and a production build does not pay for them at all.
 *
 * This file is the only writer of `document.documentElement.lang`, and the only
 * place a locale is ever computed — no consumer may derive one of its own. The
 * pre-paint script in `index.html` may have tagged the document before this
 * module runs; boot *adopts* that tag (see `prePaintTag`) rather than painting
 * over it, because stamping the fallback back is the flash the script exists to
 * prevent.
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
     *
     * A reactive `Map`, not a plain one. The tag alone is not enough to re-render
     * the app: `boot.ts` applies the cached catalog under a tag, and `load-pack.ts`
     * then hands the same tag the freshly fetched one — same tag, different bytes.
     * With a plain `Map` that second write changes nothing the UI is watching, and
     * the app keeps showing a stale catalog until the locale itself moves. Every
     * lookup reads this map through `packCatalog()`, so the read is the dependency.
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
 * The tag the document was painted with, if it is not the fallback.
 *
 * Nothing but the pre-paint script in `index.html` can put a non-`en` tag here
 * before this module runs — it reads the tag `i18n/cache.ts` mirrors into
 * `localStorage`, so the first frame is already in the last known language.
 * Adopting it is what makes that step real: syncing `en` over it here (which is
 * what this file used to do) undid the hint one frame after the script set it,
 * and a reload flashed back through English between two Chinese frames.
 *
 * The document is the only input — nothing is read from storage here — so a
 * first run, or a run with the hint cleared, still starts in English. And it is
 * a tag with no catalog behind it yet: `boot.ts` gives it text from the cached
 * catalog, and the first `loadI18NFromExtensions` either confirms it or takes it
 * back.
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

/** Whether the catalog in use answers `key`, English included. */
export function hasKey(key: LocaleKey): boolean {
    return i18n.hasKey(key);
}

/** The locale to pass to `Intl.*` — the pack's tag, else `undefined`. */
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
