import type { Catalog } from './catalog';

/**
 * The English catalog of record — the fallback every other language is measured
 * against, and the catalog `t()` resolves to when the active pack has no entry
 * for a key.
 *
 * It is **empty here on purpose.** Core keeps its English in the components
 * until the UI-string conversion task moves each literal into a key and puts
 * that key's English text in this map; seeding it with invented keys now would
 * only mean the conversion task disagrees with them later. This is deliberate,
 * not unfinished: an empty map here still gives every rule in the runtime its
 * real shape (`pack → en → key`), which is what T5 needs to be testable.
 *
 * Whatever is in this map is English source text, never a translation — core
 * ships zero translated strings. Translations live in i18n extension packs
 * (`Capabilities.I18N`), and core ships no language names either: a locale tag
 * is carried through from a pack verbatim and never mapped to a display name.
 */
export const en: Catalog = {};

/**
 * The locale this app falls back to, used for `document.documentElement.lang`
 * when no pack is active. A tag, not a name — nothing here knows that "en" is
 * called English.
 */
export const DEFAULT_LOCALE = 'en';
