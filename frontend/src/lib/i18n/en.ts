import type { Catalog } from './catalog';

/**
 * The English catalog of record — the fallback every other language is measured
 * against, and the catalog `t()` resolves to when the active pack has no entry
 * for a key.
 *
 * What lives here is the English text of every key the UI actually renders, and
 * nothing else: the UI-string conversion task moves component literals into keys
 * and brings their English with them, so the map grows as components stop
 * carrying literals. It is deliberately not seeded ahead of that — a key with no
 * caller is a key nothing can verify.
 *
 * The `i18n.offer.*` block is here for a reason specific to itself: the first-run
 * language offer is only ever shown to a browser with **no pack installed**, so
 * its own text can only be read in English. A translated copy of these strings is
 * precisely what the user has not installed yet.
 *
 * Whatever is in this map is English source text, never a translation — core
 * ships zero translated strings. Translations live in i18n extension packs
 * (`Capabilities.I18N`), and core ships no language names either: a locale tag
 * is carried through from a pack verbatim and never mapped to a display name.
 */
export const en: Catalog = {
    'i18n.offer.question': 'Read MIKE in {language}?',
    'i18n.offer.detail':
        'Your browser is set to {language}. Installing the language pack switches the interface right away — '
        + 'turn it back any time in Settings → Extensions.',
    'i18n.offer.install': 'Install {language}',
    'i18n.offer.installing': 'Installing {language}…',
    'i18n.offer.never': "Don't ask again",
    'i18n.offer.failed': 'Could not install the {language} language pack',
};

/**
 * The locale this app falls back to, used for `document.documentElement.lang`
 * when no pack is active. A tag, not a name — nothing here knows that "en" is
 * called English. It is also the subtag the first-run language offer stays quiet
 * for (`$lib/utils/locale-offer.ts`): a browser already reading this language has
 * nothing to install.
 */
export const DEFAULT_LOCALE = 'en';
