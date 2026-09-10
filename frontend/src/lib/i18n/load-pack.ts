import type { Extension } from '$lib/types';
import { extBaseURL, resolveAssetURL } from '$lib/services/ui-injector.svelte';
import { parseCatalogJSON, type Catalog } from './catalog';
import { DEFAULT_LOCALE } from './en';
import { i18n } from './runtime.svelte';
import { clearCachedCatalog, writeCachedCatalog } from './cache';

/**
 * Resolving the enabled i18n extension and putting its catalog on screen. The active
 * language *is* the enabled pack that wins the ordering rule, English when there is none;
 * nothing is stored and there is no new server route — the catalog is a file in the
 * pack's own directory, served from `ext.served_url` exactly as a css pack's css_url is.
 *
 * The server-side mirror is `extensions.ActiveI18N`; both read the capability
 * declaration rather than the `type` field, skip disabled packs and order by
 * (priority, name), which is what keeps the UI and the prompt locale in agreement.
 */

/** Outcome of one reconcile, returned so callers (and tests) can assert it. */
export interface I18NLoadResult {
    /** The locale now on screen — the fallback tag when no pack is active. */
    locale: string;
    /** Served directory of the pack in use (its `served_url`, `/extensions/…`), null for English. */
    source: string | null;
    /** Why a pack did not make it. Present whenever the answer is not what the list asked for. */
    reason?: string;
    /** A previously-applied catalog from the same pack was kept rather than dropped. */
    kept?: boolean;
}

/**
 * The pack that defines the active language, or null — which is English. Ordered by
 * `manifest.priority` then name, the rule `HandleList` sorts by and the backend derives with,
 * applied here so the winner does not depend on the order a response arrived in. Keyed on
 * the capability declaration rather than `type === 'i18n'`, which is an inference.
 */
export function resolveI18NPack(extensions: Extension[]): Extension | null {
    let winner: Extension | null = null;
    for (const ext of extensions) {
        if (!ext.enabled) continue;
        if (!ext.manifest?.capabilities?.i18n) continue;
        if (!winner || packOrderedBefore(ext, winner)) winner = ext;
    }
    return winner;
}

function packOrderedBefore(a: Extension, b: Extension): boolean {
    const pa = a.manifest?.priority ?? 0;
    const pb = b.manifest?.priority ?? 0;
    if (pa !== pb) return pa < pb;
    return a.name < b.name;
}

/** What the runtime currently shows. Module state: one app, one language. */
let applied: { source: string | null; locale: string } = { source: null, locale: DEFAULT_LOCALE };
/** Guards against two overlapping reconciles applying in the wrong order. */
let reconcileSeq = 0;

/**
 * Reconcile the active language with the server's extension list.
 *
 * Called from `ExtensionManager.loadExtensions()`, which already runs on the
 * `extension_change` broadcast — so enabling a pack switches the UI and disabling
 * it switches back, with no reload and nothing else to wire up.
 */
export async function loadI18NFromExtensions(extensions: Extension[]): Promise<I18NLoadResult> {
    const me = ++reconcileSeq;
    const winner = resolveI18NPack(extensions);

    if (!winner) {
        // No enabled pack means English, and the cache goes with it so a language cannot outlive its pack.
        await clearCachedCatalog();
        if (me !== reconcileSeq) return result();
        // Answer English unconditionally. `applied` tracks only what *this*
        // reconcile cycle put on screen, and the tag can be set without a
        // catalog: `runtime.svelte.ts` adopts the pre-paint tag from
        // `index.html`, so a pack deleted while the server was down leaves a
        // document tagged with its language and nothing else to clear it.
        i18n.setCatalog(DEFAULT_LOCALE, null);
        applied = { source: null, locale: DEFAULT_LOCALE };
        return result();
    }

    const cfg = winner.manifest.capabilities!.i18n!;
    const source = extBaseURL(winner);
    const url = resolveAssetURL(winner, cfg.messages_url);

    let reason: string | undefined;
    if (!url) {
        reason = `messages_url ${JSON.stringify(cfg.messages_url)} is not a file in the extension's own directory`;
    } else {
        const fetched = await fetchCatalog(url);
        if (me !== reconcileSeq) return result(); // a newer list arrived while we were fetching
        if (fetched.ok) {
            i18n.setCatalog(cfg.locale, fetched.catalog);
            applied = { source, locale: cfg.locale };
            reportWinner(winner, cfg.locale, extensions);
            await writeCachedCatalog({ source, locale: cfg.locale, messages: fetched.catalog });
            return result();
        }
        reason = fetched.reason;
    }

    // The pack did not load.
    if (applied.source === source && i18n.catalog !== null) {
        // The same pack is already on screen and simply failed to refresh — keep
        // the working catalog rather than flashing a working UI back to English,
        // and say so. A *different* winner that fails does fall through to
        // English below: the language the list asked for is not the one on screen.
        console.error(
            `[i18n] ${source} (${cfg.locale}) could not be refreshed (${reason}); keeping the catalog already in use`,
        );
        return { ...result(), reason, kept: true };
    }

    console.error(`[i18n] ${source} (${cfg.locale}) cannot be used: ${reason}. Remaining in English.`);
    i18n.setCatalog(DEFAULT_LOCALE, null);
    applied = { source: null, locale: DEFAULT_LOCALE };
    await clearCachedCatalog();
    return { ...result(), reason };
}

function result(): I18NLoadResult {
    return { locale: i18n.locale, source: applied.source };
}

/**
 * Say which pack won. Two enabled packs is a supported configuration with a
 * deterministic answer, and the answer should be in the log rather than
 * something the user infers from which language happened to appear.
 */
function reportWinner(winner: Extension, locale: string, extensions: Extension[]): void {
    const others = extensions.filter(
        (e) => e.enabled && e.manifest?.capabilities?.i18n && e.name !== winner.name,
    );
    if (others.length === 0) {
        console.info(`[i18n] ${winner.name} → ${locale}`);
        return;
    }
    console.info(
        `[i18n] ${winner.name} → ${locale} (active); ignoring ${others.length} other enabled pack(s): ` +
            `${others.map((o) => `${o.name} (${o.manifest.capabilities!.i18n!.locale}, priority ${o.manifest.priority ?? 0})`).join(', ')} — ` +
            `the winner is the lowest priority, then the first name`,
    );
}

/** Fetch and validate one catalog body. Never throws: a reason comes back instead. */
async function fetchCatalog(url: string): Promise<{ ok: true; catalog: Catalog } | { ok: false; reason: string }> {
    let res: Response;
    try {
        res = await fetch(url, { headers: { Accept: 'application/json' } });
    } catch (e) {
        return { ok: false, reason: `fetch of ${url} failed: ${e instanceof Error ? e.message : String(e)}` };
    }
    if (!res.ok) return { ok: false, reason: `${url} answered ${res.status}` };
    try {
        return parseCatalogJSON(await res.text());
    } catch (e) {
        return { ok: false, reason: `${url} could not be read: ${e instanceof Error ? e.message : String(e)}` };
    }
}
