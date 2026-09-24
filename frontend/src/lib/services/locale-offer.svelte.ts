import type { RegistryEntry } from '$lib/types';
import { getRegistryIndex, installExtension } from '$lib/api';
import { dialogStore } from '$lib/stores';
import { loadAppSetting, saveAppSetting } from '$lib/storage';
import { t } from '$lib/i18n/runtime.svelte';
import { identityKey } from '$lib/utils/extension-identity';
import {
    LOCALE_OFFER_SETTING_KEY,
    entryDisplayName,
    resolveLocaleOffer,
    type LocaleOfferOutcome,
    type LocaleOfferRecord,
} from '$lib/utils/locale-offer';
import { extensionManager } from './extension-manager.svelte';

/**
 * The first-run language offer as app state. The policy is pure
 * (`$lib/utils/locale-offer.ts`); this gathers the facts and owns the install.
 *
 *  - Detect after `loadExtensions()`: run earlier, the "no pack enabled" check reads
 *    stale state. One pass per boot (`checked`) — loadExtensions also runs on every
 *    `extension_change`, and an offer that returns after being answered is a nag.
 *  - A registry that will not answer is silence, not an error: this calls
 *    `getRegistryIndex()` directly so its `state.error` cannot light up Settings with a
 *    failure the user never asked about.
 *  - The click is the consent; the install reuses the git-pinned registry path.
 */

/** What the chrome renders. `idle` means "nothing to show", whatever the reason. */
type LocaleOfferPhase = 'idle' | 'offered' | 'installing' | 'failed';

interface LocaleOfferState {
    phase: LocaleOfferPhase;
    /** The pack on offer — its identity is what gets remembered, and its `display_name` is what is shown. */
    entry: RegistryEntry | null;
    /** The pack's own name, from the registry index. */
    displayName: string;
    /** The locale tag the pack declares, carried through verbatim. */
    locale: string;
    /** Install progress from the operation's SSE stream, 0-100. */
    progress: number;
    /** The latest progress message from that stream. */
    message: string;
}

function idleState(): LocaleOfferState {
    return { phase: 'idle', entry: null, displayName: '', locale: '', progress: 0, message: '' };
}

class LocaleOfferStore {
    state = $state<LocaleOfferState>(idleState());

    /** One detection pass per boot — see the class comment. */
    private checked = false;

    /**
     * Ask the four questions and show the offer if all four hold.
     *
     * `language` exists so a test can hand in a browser language; production
     * reads `navigator.language`.
     */
    async detect(language?: string): Promise<void> {
        if (this.checked) return;
        this.checked = true;

        const browserLanguage = language ?? (typeof navigator === 'undefined' ? '' : navigator.language ?? '');

        // The record is read before the registry: a user who already answered —
        // or a pack that already failed to install — must not cost a registry
        // fetch on every boot. The decision is still made by the resolver, so
        // there is exactly one place that knows when to speak.
        const record = await readRecord();
        const registry = record ? null : await readRegistry();

        const decision = resolveLocaleOffer({
            browserLanguage,
            registry,
            extensions: extensionManager.state.extensions,
            record,
        });
        if (!decision.offer) {
            // Every "no" is legitimate, so it is a log line and not a message:
            // the reason is what you would want when an offer that should have
            // appeared did not (or one that should not have, did).
            console.info(`[i18n] no language pack offered (${decision.reason})`);
            return;
        }
        this.state = {
            phase: 'offered',
            entry: decision.entry,
            displayName: decision.displayName,
            locale: decision.locale,
            progress: 0,
            message: '',
        };
    }

    /**
     * Install the offered pack over the registry's SSE progress stream, then let
     * the language follow the enabled extension.
     *
     * A successful install lands enabled (`installInBackground` stores
     * `Enabled: true`), the server broadcasts `extension_change` for it, and that
     * broadcast already drives `loadExtensions()` — which is the live switch. The
     * `loadExtensions()` here is the same call through the path we own, so the
     * switch does not depend on the socket having arrived first.
     */
    async install(): Promise<void> {
        const entry = this.state.entry;
        if (!entry || this.state.phase === 'installing') return;
        const pack = identityKey(entry.author, entry.name);

        this.state = { ...this.state, phase: 'installing', progress: 0, message: '' };
        try {
            for await (const ev of installExtension(entry.name)) {
                this.state = {
                    ...this.state,
                    progress: typeof ev.progress === 'number' ? ev.progress : this.state.progress,
                    message: ev.message || this.state.message,
                };
                if (ev.done && ev.error) throw new Error(ev.error);
            }
            await remember(pack, 'installed');
            this.state = idleState();
            await extensionManager.loadExtensions();
        } catch (e: any) {
            // Stay English, say so once, and remember the failure: a registry or a
            // pack that is broken must not re-ask on every boot.
            const message = e?.message || String(e);
            console.error(`[i18n] could not install ${pack}: ${message}`);
            await remember(pack, 'failed');
            this.state = idleState();
            dialogStore.showToast(t('i18n.offer.failed', { language: entryDisplayName(entry) }), 'error');
        }
    }

    /** "Don't ask again" — the only dismissal there is. */
    async dismiss(): Promise<void> {
        const entry = this.state.entry;
        this.state = idleState();
        if (entry) await remember(identityKey(entry.author, entry.name), 'dismissed');
    }

    /** Forget the offer *and* the one-pass guard. For tests only. */
    reset(): void {
        this.state = idleState();
        this.checked = false;
    }
}

/** Read the dismissal record. A record we cannot understand is treated as no record. */
async function readRecord(): Promise<LocaleOfferRecord | null> {
    try {
        const raw = await loadAppSetting<LocaleOfferRecord | null>(LOCALE_OFFER_SETTING_KEY, null);
        if (!raw || typeof raw !== 'object') return null;
        if (raw.outcome !== 'dismissed' && raw.outcome !== 'installed' && raw.outcome !== 'failed') return null;
        return { pack: String(raw.pack ?? ''), outcome: raw.outcome };
    } catch (e) {
        // Storage that will not answer is not a reason to nag: the safe reading of
        // "unknown" is "we already asked", so a broken IndexedDB stays silent
        // rather than offering on every boot.
        console.warn('[i18n] could not read the language-offer record; not offering', e);
        return { pack: '', outcome: 'dismissed' };
    }
}

/** The registry index, or null when it could not be read — which is silence, not an error. */
async function readRegistry(): Promise<RegistryEntry[] | null> {
    try {
        const index = await getRegistryIndex();
        return Array.isArray(index) ? index : null;
    } catch (e) {
        console.info('[i18n] registry index unavailable; no language pack offered', e);
        return null;
    }
}

/** Remember the answer. A storage failure here is logged, not surfaced. */
async function remember(pack: string, outcome: LocaleOfferOutcome): Promise<void> {
    try {
        await saveAppSetting<LocaleOfferRecord>(LOCALE_OFFER_SETTING_KEY, { pack, outcome });
    } catch (e) {
        console.warn(`[i18n] could not remember the language-pack answer (${outcome})`, e);
    }
}

export const localeOffer = new LocaleOfferStore();
