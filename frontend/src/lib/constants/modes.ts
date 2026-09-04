import { t } from '$lib/i18n';

/**
 * Descriptions for each operating mode, shown in UI selectors.
 *
 * Getters rather than literals: the copy lives in the catalog, so a translation
 * pack changes it and a read inside a `$derived`/template re-renders when the
 * locale changes — while the export keeps its exact `Record<string, string>`
 * shape, so every `MODE_DESCRIPTIONS[mode]` read site stays as it is.
 */
export const MODE_DESCRIPTIONS: Record<string, string> = {
    get act() { return t('app.mode.act'); },
    get plan() { return t('app.mode.plan'); },
    get explore() { return t('app.mode.explore'); },
};
