/**
 * The local calendar day as a number that sorts. Compared, never shown — so no `Intl` and nothing
 * locale-dependent at module scope. Both once-a-day gates stamp this value: `app-update.ts` stamps the
 * browser's own assets, `update-check.svelte.ts` stamps the download of a release. The release check's
 * own day is not here — it belongs to the install and lives in `backend/updates`.
 */
export function dayKey(): number {
    const now = new Date();
    return (now.getFullYear() * 100 + now.getMonth() + 1) * 100 + now.getDate();
}
