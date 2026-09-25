/**
 * The reconnect banner as app state: what the socket is doing, the sentence that says so, and the two
 * clocks that keep that sentence current while the socket is down — a countdown to the next attempt, and
 * the age of the data on screen.
 *
 * Only the callbacks in `handlers` write the outage state, and they are what every `createAppStream` is
 * given; the connection flag itself is published through `connectionStore`, because a reader that is not
 * this banner (`TaskChat`) reacts to its false→true edge and must not have to know about this one.
 *
 * Nothing here decides whether a request failed: `apiFetch` owns that (docs/reconnection.md → *How a
 * request ends*). `setOfflineStatus` here is the socket's reason for holding writes, and only the socket
 * edge releases it. The banner's copy is one message per state change;
 * the countdown and the age line change every second and stay out of the announced text.
 */
import type { AppStream } from '$lib/api';
import { setOfflineStatus } from '$lib/api';
import { t } from '$lib/i18n';
import { connectionStore } from '$lib/stores/connection.svelte';
import { dataAgeText } from '$lib/utils/format-time';

/** What the banner asks of the app it reports on. */
interface BannerActions {
    /** The app stream this page holds, or null when it holds none — nothing to reconnect. */
    stream(): AppStream | null;
    /** Re-read projects, the board on screen and the config. No URL change, no flicker. */
    refresh(): void;
}

/** Before the app configures the banner there is no stream and nothing to re-read. */
const NOTHING_TO_DO: BannerActions = {
    stream: () => null,
    refresh: () => {},
};

class ReconnectBanner {
    /** Epoch ms of the next scheduled attempt; 0 when nothing is scheduled. */
    nextReconnectAt = $state(0);
    countdownSeconds = $state(0);
    /** "Data from X ago", re-rendered every second while disconnected. */
    staleTimeText = $state('');
    /** Epoch ms of the last data that actually landed. A mirror landing keeps the mirror's own `cachedAt`,
     *  and a dropped socket moves nothing — the age is about the data, not about the connection. */
    lastSyncedAt = $state<number | null>(null);
    /** Whether the first load has finished: the first connect has nothing to catch up on. */
    initialLoadComplete = $state(false);

    private hadConnection = $state(false);
    private everDropped = $state(false);
    private countdownTimer: ReturnType<typeof setInterval> | null = null;
    private staleTimer: ReturnType<typeof setInterval> | null = null;
    private actions: BannerActions = NOTHING_TO_DO;

    /** Hand the banner the stream it reconnects and the re-read a reconnect owes. */
    configure(actions: BannerActions): void {
        this.actions = actions;
    }

    /** The reconnect callbacks one app stream is given — spread them into `createAppStream`. */
    readonly handlers = {
        onConnected: (connected: boolean) => this.onConnected(connected),
        onReconnectScheduled: (nextAttemptAt: number) => {
            this.nextReconnectAt = nextAttemptAt;
            this.startCountdown();
        },
        onConnectAttempt: () => {
            this.stopCountdown();
            this.countdownSeconds = 0;
        },
    };

    /** The whole line, not a number handed to the markup: word order and the unit are the translator's. */
    get status(): string {
        return this.countdownSeconds > 0
            ? t('app.reconnect.statusCountdown', { seconds: this.countdownSeconds })
            : t('app.reconnect.status');
    }

    /** One sentence per transition, announced. The countdown and the age line are not in it. */
    get notice(): string {
        if (!connectionStore.connected) {
            return this.everDropped ? t('app.reconnect.status') : '';
        }
        return this.everDropped && this.hadConnection ? t('app.reconnect.back') : '';
    }

    /** The stream's connection edge: the only writer of `connectionStore`. */
    onConnected(connected: boolean): void {
        connectionStore.set(connected);
        setOfflineStatus(!connected);
        if (connected) {
            this.hadConnection = true;
            this.stopCountdown();
            this.stopStaleTick();
            this.nextReconnectAt = 0;
            this.countdownSeconds = 0;
            // The first connect has nothing to catch up on.
            if (this.initialLoadComplete) {
                this.actions.refresh();
            }
        } else {
            this.everDropped = true;
            // The age counts from the last data that landed, not from this socket dropping.
            this.startStaleTick();
        }
    }

    /** Skip the backoff and ask now. */
    reconnectNow(): void {
        const stream = this.actions.stream();
        if (!stream) return;
        stream.reconnectNow();
        this.nextReconnectAt = 0;
    }

    /** The cache banner's way back: reconnect, and put live data on screen if the socket is already up.
     *  A refresh that answers from the mirror leaves the banner exactly where it was. */
    retryLive(): void {
        this.reconnectNow();
        this.actions.refresh();
    }

    /** Both clocks stop with the page that started them. */
    destroy(): void {
        this.stopCountdown();
        this.stopStaleTick();
    }

    /** Forget everything. For tests, which boot the banner more than once per module instance. */
    reset(): void {
        this.destroy();
        this.hadConnection = false;
        this.everDropped = false;
        this.nextReconnectAt = 0;
        this.countdownSeconds = 0;
        this.staleTimeText = '';
        this.lastSyncedAt = null;
        this.initialLoadComplete = false;
        this.actions = NOTHING_TO_DO;
    }

    private startCountdown(): void {
        this.stopCountdown();
        this.updateCountdown();
        this.countdownTimer = setInterval(() => this.updateCountdown(), 250);
    }

    private stopCountdown(): void {
        if (this.countdownTimer) {
            clearInterval(this.countdownTimer);
            this.countdownTimer = null;
        }
    }

    private updateCountdown(): void {
        this.countdownSeconds = Math.max(0, Math.ceil((this.nextReconnectAt - Date.now()) / 1000));
    }

    private startStaleTick(): void {
        this.stopStaleTick();
        this.updateStaleTime();
        this.staleTimer = setInterval(() => this.updateStaleTime(), 1000);
    }

    private stopStaleTick(): void {
        if (this.staleTimer) {
            clearInterval(this.staleTimer);
            this.staleTimer = null;
        }
    }

    private updateStaleTime(): void {
        this.staleTimeText = dataAgeText(this.lastSyncedAt);
    }
}

/** One banner per page, driven by whichever app stream is live. */
export const reconnectBanner = new ReconnectBanner();
