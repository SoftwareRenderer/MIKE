import type { Catalog } from './catalog';
import { DEFAULT_LOCALE, en } from './en';

/**
 * The i18n runtime: where the active language lives, and the only thing that
 * decides what a message id renders as.
 *
 * The active language is **derived from the enabled i18n extension** — nothing
 * stores it, and there is no `language` setting to keep in sync. `setCatalog` is
 * the whole write surface: the loader (`load-pack.ts`) resolves the enabled pack,
 * fetches and validates its catalog, and hands it here.
 *
 * `locale` and `catalog` are `$state`, so any component that calls `t()` while
 * rendering re-renders when a pack is enabled or disabled. That is the entire
 * live-switch mechanism: no version bumper, no `$effect` mirror, no reload.
 */

/** Interpolation parameters for a message: `{name}` tokens in the text. */
export type MessageParams = Record<string, string | number>;

export class I18nRuntime {
    /** The locale the UI is currently rendering. */
    locale = $state(DEFAULT_LOCALE);

    /**
     * The active pack's catalog, or null when no pack is active — which is not
     * "English mode" as a special case, just "nothing to fall back past `en`".
     */
    catalog = $state<Catalog | null>(null);

    /**
     * Keys already warned about for the current catalog. A missing key is a
     * defect in the pack, reported once rather than on every render — and reset
     * when a new catalog arrives, because a reloaded pack may have fixed it.
     */
    private warned = new Set<string>();

    /**
     * Install (or remove) a catalog. `messages` is a finished, validated object:
     * one assignment, so the UI can never observe a half-loaded pack — either the
     * whole catalog is in place or the previous state is.
     *
     * Passing `null` returns to English and is what a missing, disabled, or
     * failed-to-load pack looks like from here.
     */
    setCatalog(locale: string, messages: Catalog | null): void {
        // Snapshot the catalog: the loader builds the object and hands it over,
        // and from this point the UI renders a fixed set of strings. Without the
        // copy, a loader mutating its working object mid-load would change what
        // is on screen one key at a time — the half-applied pack in another
        // disguise.
        const catalog =
            messages && locale && locale !== DEFAULT_LOCALE ? ({ ...messages } as Catalog) : null;
        this.locale = catalog ? locale : DEFAULT_LOCALE;
        this.catalog = catalog;
        this.warned.clear();
        this.applyDocumentLocale();
    }

    /**
     * Resolve a message id.
     *
     * `pack → en → the id itself`. The last step is the only way a missing key
     * can reach the screen, and it is always a bug in a pack or in this file:
     * when a pack is active and cannot answer, the English text is used and the
     * miss is warned once, so a partially translated pack degrades per key
     * instead of producing a UI of raw ids or a half-English wall.
     *
     * Reactive because it reads `$state` fields — call it from a template or
     * `$derived` and the reader updates with the active pack.
     */
    t(key: string, params?: MessageParams): string {
        const active = this.catalog;
        let text = active ? active[key] : undefined;
        if (text === undefined) {
            text = en[key];
            // Only warn while a pack is active: with no pack this is English,
            // and an id missing from `en` is the conversion task's to-do list,
            // not a runtime fault. (en is deliberately empty until that task.)
            if (active && !this.warned.has(key)) {
                this.warned.add(key);
                console.warn(
                    `[i18n] ${this.locale} catalog has no entry for ${JSON.stringify(key)}; ` +
                        `falling back to ${text === undefined ? 'the message id' : 'English'}`,
                );
            }
        }
        if (text === undefined) return key;
        return params ? interpolate(text, params) : text;
    }

    /**
     * Whether the active pack answers `key` — useful for callers that must not
     * show a label at all rather than show English one.
     */
    has(key: string): boolean {
        return this.catalog !== null && this.catalog[key] !== undefined;
    }

    /** Mirror the active locale onto the document, which owns it from here. */
    private applyDocumentLocale(): void {
        if (typeof document === 'undefined') return;
        document.documentElement.lang = this.locale;
    }
}

/** Replace `{name}` tokens. Unknown tokens are left alone — a missing parameter
 *  should read as the template, not as an empty string hiding a bug. */
function interpolate(text: string, params: MessageParams): string {
    return text.replace(/\{(\w+)\}/g, (match, name: string) =>
        Object.prototype.hasOwnProperty.call(params, name) ? String(params[name]) : match,
    );
}

/** The app-wide runtime. */
export const i18n = new I18nRuntime();

/**
 * Translate a message id against the active pack. Reads the runtime's `$state`,
 * so a component calling this re-renders when the enabled pack changes.
 */
export function t(key: string, params?: MessageParams): string {
    return i18n.t(key, params);
}

/** The locale the UI is rendering — the enabled pack's, or the fallback. */
export function activeLocale(): string {
    return i18n.locale;
}
