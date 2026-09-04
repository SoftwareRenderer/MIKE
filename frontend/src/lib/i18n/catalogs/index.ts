/**
 * The catalog table: locale tag → message catalog.
 *
 * `en` is the catalog of RECORD — every key the app uses exists here first, with
 * values byte-identical to the strings that were hardcoded before i18n. Other
 * locales are partial by design: a pack that ships only half the keys is fine, the
 * runtime answers the rest (see `packText` in `../runtime.svelte`).
 *
 * Keys are compared case-insensitively and narrowed by subtag (`zh-Hans-CN` falls
 * back to `zh-Hans`, then `zh`), so a pack may register under whichever tag its
 * manifest declares.
 *
 * THIS IS THE T5 SEAM. Installed language packs get merged in here; nothing else
 * about the runtime needs to change when they do.
 */
import { en, type Catalog, type MessageKey } from './en';

export type { Catalog, MessageKey };

export const DEFAULT_LOCALE = 'en';

export const catalogs: Record<string, Catalog> = {
    [DEFAULT_LOCALE]: en,
};
