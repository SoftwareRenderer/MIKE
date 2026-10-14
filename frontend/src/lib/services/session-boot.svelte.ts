/**
 * Which of the three screens this browser gets — the app, the offline mirror, or the login form — and the
 * clicks that move between them. `/api/auth/session` decides (docs/reconnection.md → *The boot offer and
 * one stream per page*): a 2 s timer only *offers* the mirror when the mirror holds projects, and the
 * answer that arrives afterwards takes the offer back, so a slow server is described as slow rather than
 * as unreachable. `bootSlow` is what first paint waits behind.
 *
 * The stream and the board are not this module's: both boot paths hand them to the app through
 * `configure`, because the one live `createAppStream` and the stores it writes live there. What this module
 * owns is the order — session check → cached board → live board — and the flags that say which of the three
 * the screen is showing.
 *
 * A session check that throws is a server that did not answer, so it is the same branch as a mirror offer:
 * mark the app offline and read the board out of IndexedDB. A cancellation is not an outage and `apiFetch`
 * does not raise the flag for one (docs/reconnection.md → *How a request ends*); nothing here adds a
 * deadline of its own.
 */
import { checkSession, clearQueue, logout, setOfflineStatus } from '$lib/api';
import type { Project } from '$lib/types';
import { clearAll, getBoardCache, getBoardCacheEntry } from '$lib/storage';
import { extensionManager } from './extension-manager.svelte';
import { localeOffer } from './locale-offer.svelte';
import { reconnectBanner } from './reconnect-banner.svelte';
import { urlRouter } from './url-router.svelte';

/** How long the session check may take before the mirror is offered. An offer, never a verdict. */
const BOOT_OFFER_MS = 2000;

/** The app-owned work each boot path needs: one stream, then one board. */
interface BootActions {
    /** Close the stream this page holds, if any. One app stream per page — a second one leaves the first
     *  connected, still firing handlers, and unreachable to close. */
    closeStream(): void;
    /** Open the app stream. `mirror` is the offline screen's own: its task handlers stay silent until a
     *  live board replaces the mirrored one. */
    openStream(mode: 'live' | 'mirror'): void;
    /** Flush this browser's queued writes, then read projects, the board on screen, the config, counts.
     *  `mirror` is the offline screen's load, where nothing waits on the network: a request that never
     *  answers would otherwise sit between the click and the board. */
    loadAppData(mode: 'live' | 'mirror'): Promise<void>;
}

/** Before the app configures the boot there is no stream to open and no board to read. */
const NOTHING_TO_DO: BootActions = {
    closeStream: () => {},
    openStream: () => {},
    loadAppData: async () => {},
};

class SessionBoot {
    /** The login gate. True means the app is on screen. */
    isAuthenticated = $state(false);
    /** Whether this install has any passkey, from the same answer that decided `isAuthenticated`. It is
     *  the login form's branch — a fresh install has no credential to ask for, so it asks for the pairing
     *  code instead. `null` is the server's "could not read it", which shows the sign-in branch. */
    hasPasskey = $state<boolean | null>(null);
    /** Whether this server has its own CA to hand out, from the same answer. It decides whether the
     *  login gate can offer a browser the certificate that would make this connection secure. */
    caDownload = $state(false);
    /** What this browser reads as, from the same answer — the name the pairing form opens with. */
    deviceName = $state('');
    /** First paint is this module's, not the board's: the session check decides what to paint. */
    isLoadingAuth = $state(true);
    /** The session check has not answered, so the wait is what the loading screen says. */
    bootSlow = $state(false);
    /** The server did not answer and the mirror holds projects, so the mirror can be offered. */
    hasLocalCache = $state(false);
    /** What is on screen came out of the mirror — while the socket stayed up, which is why the
     *  reconnecting banner is off and this banner carries its own way back. */
    servingFromCache = $state(false);

    private actions: BootActions = NOTHING_TO_DO;

    /** Hand the boot the app's stream and its board read. */
    configure(actions: BootActions): void {
        this.actions = actions;
    }

    /** The boot every page starts with: ask the server, and offer the mirror only if it is slow. */
    async boot(): Promise<void> {
        // A slow session answer is not an unreachable server: the 2 s timer only offers the offline
        // mirror, and the answer that arrives later takes the offer back.
        let authResolved = false;
        const offlineTimer = setTimeout(async () => {
            if (authResolved) return;
            this.bootSlow = true;
            setOfflineStatus(true);
            // With no mirror to offer there is nothing to show but the wait.
            if (await this.hasCachedProjects()) {
                this.hasLocalCache = true;
                this.isLoadingAuth = false;
            }
        }, BOOT_OFFER_MS);
        try {
            const session = await checkSession();
            this.hasPasskey = session.hasPasskey;
            this.caDownload = session.caDownload;
            this.deviceName = session.deviceName;
            this.isAuthenticated = session.authenticated;
            authResolved = true;
            clearTimeout(offlineTimer);
            this.bootSlow = false;
            // The mirror screen outranks the app, so only this takes it down.
            this.hasLocalCache = false;
            if (this.isAuthenticated) {
                await this.startSession();
            }
            this.isLoadingAuth = false;
        } catch (e) {
            authResolved = true;
            clearTimeout(offlineTimer);
            console.error('Failed to check auth:', e);
            // The boot probe got no answer, so writes wait; reads still try the network.
            setOfflineStatus(true);
            this.hasLocalCache = await this.hasCachedProjects();
            this.isLoadingAuth = false;
        }
    }

    /** The signed-in boot: one stream, the URL's project, then the board. */
    async startSession(): Promise<void> {
        this.actions.closeStream();
        this.actions.openStream('live');
        urlRouter.read();
        urlRouter.applyView();
        await this.actions.loadAppData('live');
        this.offerLanguagePack();
    }

    /** The login form's answer: there is a session now, so the app replaces the form. */
    async startAfterLogin(): Promise<void> {
        this.isAuthenticated = true;
        // A session exists only behind a finished ceremony, so this browser holds a passkey whatever the
        // boot answer said — and after a sign-out the form must ask for that passkey, not for a code.
        this.hasPasskey = true;
        await this.startSession();
    }

    /** "Offline Mode": show the mirror, and count the data's age from the moment it was written. */
    async useLocalCache(): Promise<void> {
        this.hasLocalCache = false; // hide the offline screen
        this.actions.closeStream(); // the mirror's stream must not outlive the mirror
        // The offline screen hides the loading screen, so raise it again: the app shell underneath has no
        // project and no config, and shows its empty state while the mirror is read.
        this.isLoadingAuth = true;
        this.isAuthenticated = true;
        this.actions.openStream('mirror');
        const mirror = await getBoardCacheEntry('projects').catch(() => null);
        if (mirror) reconnectBanner.lastSyncedAt = mirror.cachedAt;
        await this.actions.loadAppData('mirror'); // marks the initial load complete
        this.isLoadingAuth = false; // hide loading screen after data is loaded
        this.offerLanguagePack();
    }

    /** Signing out means this browser keeps nothing: the stores hold this user's messages, diffs, drafts
     *  and queued writes, and `gc()` only prunes tasks that no longer exist. The in-memory queue goes with
     *  them — a queue left behind is written back into the emptied database by the next enqueue. */
    async signOut(): Promise<void> {
        await logout();
        clearQueue();
        await clearAll();
        this.isAuthenticated = false;
    }

    /** Forget every flag. For tests, which boot the module more than once per instance. */
    reset(): void {
        this.isAuthenticated = false;
        this.hasPasskey = null;
        this.caDownload = false;
        this.deviceName = '';
        this.isLoadingAuth = true;
        this.bootSlow = false;
        this.hasLocalCache = false;
        this.servingFromCache = false;
        this.actions = NOTHING_TO_DO;
    }

    /** Is there a mirrored project list to offer? A mirror that will not answer is no mirror. */
    private async hasCachedProjects(): Promise<boolean> {
        try {
            const cached = await getBoardCache<Project[]>('projects');
            return !!cached && cached.length > 0;
        } catch (e) {
            console.error('Failed to check cached data:', e);
            return false;
        }
    }

    /** Installed extensions feed LSP discovery, and then the browser is asked (once, silently if the
     *  answer is no) whether it wants a translation pack. The offer runs **after** the extension list
     *  resolves: its first rule is "no i18n pack is enabled", and before this resolves that answer is the
     *  previous session's, which offers a pack that is already on. */
    private offerLanguagePack(): void {
        extensionManager.loadExtensions()
            .then(() => localeOffer.detect())
            .catch(() => {});
    }
}

/** One boot per page. */
export const sessionBoot = new SessionBoot();
