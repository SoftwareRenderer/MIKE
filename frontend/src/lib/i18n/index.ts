/**
 * `$lib/i18n` — the only import site code needs. `t(key, params?)` is the whole surface
 * a component uses; keys are typed off `en.ts`, so a typo is a build error. `setCatalog`
 * / `resetToEnglish` are exported for the pack-loading path and its tests.
 *
 * Nothing else is re-exported: a barrel that re-exports internals gives each of them a
 * second name, and the second name is the one that goes stale. The locale questions
 * (`activeLocale`, `documentLanguage`) live in `./runtime.svelte`, where the difference
 * between them is documented.
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
