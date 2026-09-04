/**
 * `$lib/i18n` — the only import site code needs.
 *
 * `t(key, params?)` is the whole surface a component uses; `locale`, `setCatalog` and
 * `resetToEnglish` belong to whoever installs a translation pack (Settings → i18n, and
 * the `i18n` extension type). Message keys are typed off `en.ts`, so a typo is a build
 * error rather than a string on screen.
 */
export { t, locale, setCatalog, resetToEnglish } from './runtime.svelte';
export type { MessageKey, MessageParams, MessageValue, PluralTable, PluralCategory } from './en';
