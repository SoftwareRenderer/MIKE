/**
 * `$lib/i18n` — the only import site code needs.
 *
 * `t(key, params?)` is the whole surface a component uses; everything else belongs
 * to whoever installs a translation pack (Settings → Extensions, the `i18n`
 * extension type, and `$lib/i18n/load-pack.ts`). Message keys are typed off
 * `en.ts`, so a typo is a build error rather than a string on screen.
 *
 * Two questions, deliberately different answers:
 *   `activeLocale()`     — the locale to hand `Intl.*` (`undefined` = use the browser's)
 *   `documentLanguage()` — the language of the strings actually rendered (`en` with no pack)
 */
export {
    // the resolution surface
    t,
    locale,
    // the pack-loading surface
    i18n,
    setCatalog,
    resetToEnglish,
    registerCatalog,
    setLocale,
    // the locale surface
    activeLocale,
    resolvedLocale,
    documentLanguage,
    onLocaleChange,
    // pack-only lookups (a pack may address its own keys)
    packText,
    has,
    hasKey,
} from './runtime.svelte';
export { I18nRuntime, type LocaleKey } from './runtime.svelte';
export { DEFAULT_LOCALE, en } from './en';
export type { MessageKey, MessageParams, MessageValue, PluralTable, PluralCategory } from './en';
export type { Catalog } from './catalog';
