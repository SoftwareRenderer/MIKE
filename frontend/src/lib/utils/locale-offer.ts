import type { Extension, RegistryEntry } from '$lib/types';
import { resolveI18NPack } from '$lib/i18n/load-pack';
import { DEFAULT_LOCALE } from '$lib/i18n/en';
import { identityKey } from './extension-identity';

/**
 * The first-run language offer: the decision of whether to ask a browser whose
 * language is not the fallback locale to install the registry's pack for it.
 *
 * Everything here is pure — the resolver takes the three facts it needs plus the
 * dismissal record and returns a decision, so the whole policy is testable
 * without a browser, a server, or a registry. The service
 * (`$lib/services/locale-offer.svelte.ts`) is what gathers those facts and owns
 * the install; the component only renders the decision.
 *
 * Four conditions must all hold, and **silent is the correct answer to every
 * other case**:
 *  1. no i18n pack is enabled — English *is* the no-pack state, so an enabled pack
 *     already means the user has a language and made their choice;
 *  2. the browser's primary language subtag is not the fallback locale;
 *  3. the registry index carries a pack whose declared `capabilities.i18n.locale`
 *     answers that subtag — the pack's locale is one bare subtag ("de"), which is
 *     why one browser language can only ever name one language to offer;
 *     no match, no word, ever;
 *  4. nothing has been asked before: a dismissed, installed *or failed* record
 *     ends the conversation. The failure case matters most: a registry that is
 *     unreachable, or a pack that will not install, must not nag on every boot.
 *
 * What core holds about languages: one tag (`DEFAULT_LOCALE`, from `en.ts`) and
 * the shape of a BCP 47 tag. No language names — the pack's own `display_name`
 * is what the offer shows, verbatim, in its own script.
 */

/** Key in the IndexedDB `app-settings` store (via `$lib/storage`, never IndexedDB directly). */
export const LOCALE_OFFER_SETTING_KEY = 'locale-offer';

/** Why the record exists. All three suppress the offer; only `installed` is a success. */
export type LocaleOfferOutcome = 'dismissed' | 'installed' | 'failed';

/** What was remembered about the last ask. */
export interface LocaleOfferRecord {
    /** Identity key (`author/name`) of the pack the user was asked about. */
    pack: string;
    outcome: LocaleOfferOutcome;
}

/** Why nothing is offered. Kept in the decision so a caller can log the reason. */
export type LocaleOfferReason =
    /** A dismissed/installed/failed record exists — we already asked. */
    | 'already-asked'
    /** An enabled pack already defines the language. */
    | 'pack-enabled'
    /** The browser speaks the fallback locale, or the tag is unreadable. */
    | 'english-browser'
    /** No registry entry declares a pack for that language (includes: no registry). */
    | 'no-matching-pack'
    /** The only matching pack is already installed — see `installedEntry` below. */
    | 'installed-entry';

export type LocaleOfferDecision =
    | { offer: true; entry: RegistryEntry; locale: string; displayName: string }
    | { offer: false; reason: LocaleOfferReason };

export interface LocaleOfferInput {
    /** `navigator.language` — anything else (null, `''`, garbage) reads as "no language". */
    browserLanguage: string | null | undefined;
    /** The registry index, or null/undefined when it could not be read. */
    registry: RegistryEntry[] | null | undefined;
    /** The installed extensions, exactly as `GET /api/extensions` answered. */
    extensions: Extension[];
    /** The remembered ask, if any. */
    record?: LocaleOfferRecord | null;
}

/**
 * The primary subtag of a language tag, lowercased: `sr-Latn-RS` → `sr`,
 * `de-AT` → `de`, `pt_BR` → `pt`.
 *
 * Deliberately strict about garbage: a wildcard (`*`), a private-use singleton
 * (`x-pig`), digits, or an empty string all answer `''`, which every caller reads
 * as "there is no language to offer against". `navigator.language` is
 * user-agent-controlled, so this is not a place to be generous.
 *
 * `_` is accepted as a separator because browsers hand back `pt_BR` when the
 * platform's locale is spelled that way.
 */
export function primarySubtag(tag: unknown): string {
    if (typeof tag !== 'string') return '';
    const first = tag.trim().toLowerCase().split(/[-_]/)[0] ?? '';
    return /^[a-z]{2,8}$/.test(first) ? first : '';
}

/**
 * The locale a registry entry offers, or `''` when the entry is not an offerable
 * translation pack.
 *
 * Keyed on the capability *declaration* rather than `type === 'i18n'`, which is
 * what `inferExtensionType` inferred for routing — the same rule `resolveI18NPack`
 * and `Extension.GetI18N` use, so the three ends cannot disagree about what a
 * pack is.
 */
function offeredLocale(entry: RegistryEntry): string {
    const cfg = entry.capabilities?.i18n as { locale?: unknown } | undefined;
    if (!cfg || typeof cfg.locale !== 'string') return '';
    return cfg.locale.trim();
}

/**
 * An entry is only offerable when the install path can actually install it.
 *
 * `installExtension(name)` is the registry's git-pinned install: the server looks
 * the name up in the index and clones `repo` at `commit`, and it refuses anything
 * that is not pinned (`validateGitSource`). An entry without a pin — a local
 * listing, or a hand-edited index — would be a button that can only fail.
 */
function isPinned(entry: RegistryEntry): boolean {
    return !!entry.repo && !!entry.commit;
}

/**
 * Decide whether to offer a language pack. See the header for the four rules.
 *
 * The order of the checks is the order of the reasons: the record is read first
 * because it is the cheapest answer and the one the user gave us, and the
 * language work is only done when we are still allowed to speak.
 */
export function resolveLocaleOffer(input: LocaleOfferInput): LocaleOfferDecision {
    if (input.record) return { offer: false, reason: 'already-asked' };

    // Rule 1. Not "an i18n-typed extension" but the same enabled-pack rule the
    // loader resolves the language with: asking someone who already has a pack
    // (possibly this exact one, possibly a heavier one it beats on priority)
    // would offer a second language to an app that already has one.
    if (resolveI18NPack(input.extensions)) return { offer: false, reason: 'pack-enabled' };

    // Rule 2. A browser with no readable language gets nothing: the fallback
    // locale is what the app already renders, so there is nothing to offer it.
    const want = primarySubtag(input.browserLanguage);
    if (!want || want === DEFAULT_LOCALE) return { offer: false, reason: 'english-browser' };

    // Rule 3. A missing or unreachable registry is the normal state in tests and
    // a common one offline; it is silence, never an error.
    const installed = new Set(input.extensions.map((e) => identityKey(e.author, e.name)));
    // One comparison, and it is the primary subtag on both sides: a pack may only
    // declare a bare subtag (`validateLocale` refuses a region, because "pt-BR" and
    // "pt-PT" are not one language to the reader, and one pack is one language), and
    // a `pt_BR` or `sr-Latn-RS` browser reduces to the same thing. Reading the entry
    // through the same function is deliberate tolerance — the index is a published
    // file, not a validated one, so an entry generated before that rule still reaches
    // the reader it was written for rather than making the offer go quiet.
    const matches = (entry: RegistryEntry): boolean => {
        const locale = offeredLocale(entry);
        return !!locale && primarySubtag(locale) === want;
    };

    let best: RegistryEntry | null = null;
    /** The language has a pack, but the user already has it installed. */
    let matchedInstalled = false;

    for (const entry of input.registry ?? []) {
        if (!offeredLocale(entry) || !isPinned(entry) || !matches(entry)) continue;
        // Installing a pack that is already installed answers 409, and a pack the
        // user *disabled* is a decision, not an accident: they chose to go back to
        // the fallback language, so nothing here re-offers what they turned off.
        if (installed.has(identityKey(entry.author, entry.name))) {
            matchedInstalled = true;
            continue;
        }
        // Two entries answering one language is a registry with a duplicate in it, not
        // a choice to make: take the first by name so the offer cannot depend on the
        // order the index happened to arrive in.
        if (!best || entry.name < best.name) best = entry;
    }

    if (!best) {
        return { offer: false, reason: matchedInstalled ? 'installed-entry' : 'no-matching-pack' };
    }
    return {
        offer: true,
        entry: best,
        locale: offeredLocale(best),
        // The pack's own name for itself, straight from the registry index. Core never
        // learns what a locale tag is called and never will: it has no language table to
        // translate from, and a name it invented would be a name nobody chose.
        displayName: entryDisplayName(best),
    };
}

/** The name to show for a registry entry — the pack's, never a core-invented one. */
export function entryDisplayName(entry: RegistryEntry): string {
    return entry.display_name?.trim() || entry.name;
}
