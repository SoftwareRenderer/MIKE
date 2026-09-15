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

/**
 * The toggle's tooltip — the mode value is data, this is copy. A function, not a getter record like
 * the descriptions above, because a mode the app does not know answers the explore tip.
 */
export function modeTip(mode: string): string {
    return mode === 'act' ? t('chat.mode.actTip') : mode === 'plan' ? t('chat.mode.planTip') : t('chat.mode.exploreTip');
}
