/**
 * `$lib/i18n` — the only import site code needs.
 *
 * `t(key, params?)` is the whole surface a component uses; message keys are typed
 * off `en.ts`, so a typo is a build error rather than a string on screen.
 * `setCatalog` / `resetToEnglish` are exported here as well, because the pack-loading
 * path and its tests are this module's other consumer.
 *
 * Everything else comes from the module that owns it: a barrel that re-exports the
 * internals gives each of them a second name, and the second name is the one that
 * goes stale.
 *   `./runtime.svelte` the runtime (`i18n`, `registerCatalog`, `setLocale`,
 *                       `onLocaleChange`, `I18nRuntime`) and the locale questions,
 *                       which are deliberately different answers:
 *                         `activeLocale()`     — the tag to hand `Intl.*`
 *                                               (`undefined` = use the browser's)
 *                         `documentLanguage()` — the language of the strings actually
 *                                               rendered (`en` with no pack)
 *   `./en`             the English catalog of record, `DEFAULT_LOCALE`, its key types
 *   `./catalog` · `./cache` · `./load-pack` — the pack-loading half
 */
export {
    // the resolution surface — what a component renders with
    t,
    locale,
    // the pack-loading surface — Settings, the `i18n` extension type, load-pack.ts
    setCatalog,
    resetToEnglish,
} from './runtime.svelte';
// The types `t` and `setCatalog` speak. A caller typing a params bag, a plural
// table or a catalog needs the name from the same module it calls.
export type { MessageKey, MessageParams, MessageValue, PluralTable, PluralCategory } from './en';
export type { Catalog } from './catalog';
