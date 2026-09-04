import type { Catalog } from './catalog';

/**
 * THE English catalog of record — the fallback every other language is measured
 * against, and the catalog `t()` resolves to when the active pack has no entry
 * for a key.
 *
 * What lives here is the English text of every key the UI actually renders, and
 * nothing else: the UI-string conversion task moves component literals into keys
 * and brings their English with them, so the map grows as components stop
 * carrying literals. It is deliberately not seeded ahead of that — a key with no
 * caller is a key nothing can verify. Every value is BYTE-IDENTICAL to the string
 * that used to be hardcoded at the call site it came from; `utils/__tests__/
 * format-time.unit.ts` pins several of them, and that test is what keeps this file
 * honest.
 *
 * `{placeholder}` token NAMES are part of a key's contract (a pack that drops
 * `{time}` loses the time), so they stay English even in a translated pack.
 *
 * The `i18n.offer.*` block is here for a reason specific to itself: the first-run
 * language offer is only ever shown to a browser with **no pack installed**, so
 * its own text can only be read in English. A translated copy of these strings is
 * precisely what the user has not installed yet.
 *
 * Whatever is in this map is English source text, never a translation — core ships
 * zero translated strings. Translations live in i18n extension packs
 * (`Capabilities.I18N`), and core ships no language names either: a locale tag is
 * carried through from a pack verbatim and never mapped to a display name.
 */
const catalog = {
    // --- Relative time -----------------------------------------------------
    // The `{n} ago` rungs are here for completeness (and as an escape hatch: a
    // language ICU renders badly in — French narrow is "-5 min" — overrides them).
    // `format-time` prefers Intl.RelativeTimeFormat over these for any locale that
    // does NOT ship the key, which is why they read as abbreviations, not words.
    'time.justNow': 'just now',
    'time.secondAgo': '{n}s ago',
    'time.minuteAgo': '{n}m ago',
    'time.hourAgo': '{n}h ago',
    'time.dayAgo': '{n}d ago',
    'time.weekAgo': '{n}w ago',
    'time.monthAgo': '{n}mo ago',
    'time.yearAgo': '{n}y ago',

    // --- Timestamps (chat) --------------------------------------------------
    'time.yesterdayAt': 'Yesterday at {time}',
    'time.lastWeekdayAt': 'Last {weekday} at {time}',
    'time.onDateAt': '{date} at {time}',

    // --- Wrapped phrases ----------------------------------------------------
    // Templates rather than a `prefix` string, because "Built " is not a prefix in
    // every language — the position belongs to the catalog, not the caller.
    'time.built': 'Built {when}',
    'time.saved': 'Saved {when}',

    // --- The first-run language offer (T15b) --------------------------------
    'i18n.offer.question': 'Read MIKE in {language}?',
    'i18n.offer.detail':
        'Your browser is set to {language}. Installing the language pack switches the interface right away — '
        + 'turn it back any time in Settings → Extensions.',
    'i18n.offer.install': 'Install {language}',
    'i18n.offer.installing': 'Installing {language}…',
    'i18n.offer.never': "Don't ask again",
    'i18n.offer.failed': 'Could not install the {language} language pack',
} satisfies Catalog;

/**
 * The same map, typed as a mutable `Catalog`.
 *
 * The literal type above is what `MessageKey` reads (so `t('tiem.bulit')` is a type
 * error); this is what `t()` indexes at runtime, and what a test seeds a key into.
 * One object, two views — an `en: Catalog` annotation would throw the literal keys
 * away, and `satisfies` alone would make `en[key] = …` an excess-property error.
 */
export const en: Catalog = catalog;

/** Every key the core knows about. A typo in `t('tiem.bulit')` is a type error. */
export type MessageKey = keyof typeof catalog;

/**
 * The locale this app falls back to, used for `document.documentElement.lang`
 * when no pack is active. A tag, not a name — nothing here knows that "en" is
 * called English. It is also the subtag the first-run language offer stays quiet
 * for (`$lib/utils/locale-offer.ts`): a browser already reading this language has
 * nothing to install.
 */
export const DEFAULT_LOCALE = 'en';
