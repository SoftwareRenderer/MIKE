/**
 * The server's release update as app state: whether a newer release is known, what this install may do
 * about it, and the clicks that lead from one to the other. Nothing runs on a timer — one pass per boot,
 * with the outbound check gated to once a day. The mechanism is docs/auto-update.md.
 */
import type { UpdateApplyResult, UpdatePreparedResult, UpdateRestartResult, UpdateStatus } from '$lib/types';
import { applyUpdate, checkForUpdates, fetchUpdateStatus, prepareUpdate, restartToUpdate } from '$lib/api/updates';
import { setUpdateCheckConsent } from '$lib/api/config';
import { dialogStore } from '$lib/stores';
import { loadAppSetting, saveAppSetting } from '$lib/storage';
import { t } from '$lib/i18n';

/** Also the repeat guard: a check that ran today does not run again on the next reload. */
const LAST_CHECK_DAY_KEY = 'update-check-last-day';
/** Once per browser, whatever the answer did to the server. */
const CONSENT_ASKED_KEY = 'update-check-consent-asked';
/** Which release was last fetched, and on which day. See stampPrepare. */
const PREPARE_STAMP_KEY = 'update-prepare-stamp';

/** What the Settings icon renders. `question` is the opt-in answer this install still owes, `ready` a
 *  release that is downloaded and waiting for the install click. */
export type UpdateDot = 'none' | 'question' | 'update' | 'ready';

class UpdateCheckStore {
    /** The last status this browser read. Null until the first one lands, which is not "up to date". */
    status = $state<UpdateStatus | null>(null);
    /** What the buttons are doing, so a second click cannot start the same thing twice. */
    phase = $state<'idle' | 'checking' | 'preparing' | 'applying' | 'restarting'>('idle');
    /** The last action's refusal or failure, in the server's own words when it had any. */
    fault = $state('');
    /** The release the last prepare downloaded, so the row can name it before the status catches up. */
    lastPrepared = $state<UpdatePreparedResult | null>(null);
    lastApplied = $state<UpdateApplyResult | null>(null);
    lastRestart = $state<UpdateRestartResult | null>(null);

    /** One pass per boot: the status read, the gated check and the opt-in question behind them. */
    private started = false;

    async start(): Promise<void> {
        if (this.started) return;
        this.started = true;
        await this.refresh();
        // A deployment that forbids updates hears nothing about them: no question, no check, no
        // download, and no preference written on its behalf.
        if (this.status?.disabled) return;
        // The question first: while it is unanswered checks are off, so there is nothing to ask the
        // manifest about, and a slow manifest must not delay the dialog by its own round trip.
        await this.askConsent();
        if (this.status?.enabled) {
            await this.checkOnceToday();
            // Whatever the check's own day stamp decided, the answer the server holds may still name a
            // release this install has never fetched. stampPrepare is what keeps that cheap.
            await this.autoPrepare();
        }
    }

    /** Forget the pass. For tests, which boot the store more than once in one module instance. */
    reset(): void {
        this.started = false;
    }

    /** Read what the server already knows. Performs no egress of its own. */
    async refresh(): Promise<void> {
        try {
            this.status = await fetchUpdateStatus();
        } catch {
            // Offline, or not signed in yet. The dot stays dark on a status nobody read, which is the
            // same answer as "nothing known" and the reason no message belongs here.
        }
    }

    /** The dot on the Settings icon. */
    get dot(): UpdateDot {
        const status = this.status;
        if (!status || status.disabled) return 'none';
        if (status.prepared_version) return 'ready';
        if (status.available) return 'update';
        return this.consentOwed ? 'question' : 'none';
    }

    /** Whether this install may be offered the opt-in question at all. */
    get consentOwed(): boolean {
        const status = this.status;
        return !!status && !status.answered && !status.disabled;
    }

    get busy(): boolean {
        return this.phase !== 'idle';
    }

    /** The once-a-day check, stamped before the request so a slow answer is not asked again on reload. */
    async checkOnceToday(): Promise<void> {
        if (!(await stampToday())) return;
        await this.check();
    }

    /** The button. Always asks, whatever the day stamp says. */
    async checkNow(): Promise<void> {
        await stampToday();
        if (await this.check()) await this.autoPrepare();
    }

    /** Whether the manifest answered. A check that did not is no licence to download the release an
     *  earlier one reported. */
    private async check(): Promise<boolean> {
        if (this.busy) return false;
        this.phase = 'checking';
        this.fault = '';
        let answered = false;
        try {
            this.status = await checkForUpdates();
            answered = true;
        } catch (e: any) {
            this.fault = messageOf(e);
            // The failed check changed nothing worth showing, but the last good answer is still true,
            // and the About row says when it was made — so it is re-read rather than repeated here.
            await this.refresh();
        } finally {
            this.phase = 'idle';
        }
        return answered;
    }

    /**
     * The automatic half: a newer release this install could take is fetched without being asked for.
     * Gated per release and day (stampPrepare), so a failure that will not fix itself between two reloads
     * — no space, no route to the host — is retried once a day. Installing is never decided here.
     */
    async autoPrepare(): Promise<void> {
        const st = this.status;
        if (!st?.available || !st.latest) return;
        if (!st.install?.can_stage) return;
        if (st.staged_version || st.prepared_version || st.working) return;
        if (!(await stampPrepare(st.latest))) return;
        await this.prepare();
    }

    /** Fetch and verify the newest release. The install directory keeps running; nothing is replaced. */
    async prepare(): Promise<void> {
        if (this.busy) return;
        this.phase = 'preparing';
        this.fault = '';
        let done: UpdatePreparedResult | null = null;
        try {
            done = await prepareUpdate();
            this.lastPrepared = done;
            await this.refresh();
        } catch (e: any) {
            this.fault = messageOf(e);
            await this.refresh();
        } finally {
            this.phase = 'idle';
        }
        // Asked after the download stops holding the phase, so a person reading the question is not
        // looking at a screen that believes it is still busy.
        if (done) await this.offerInstall(done.version);
    }

    /** The one thing the automatic half announces. Dismissing it is an answer, not a deferral. */
    private async offerInstall(version: string): Promise<void> {
        const now = await dialogStore.showConfirm({
            title: t('updates.ready.title', { version }),
            message: t('updates.ready.message'),
            confirmText: t('updates.ready.install'),
            cancelText: t('updates.ready.later'),
        });
        if (now) await this.apply();
    }

    /** *Install*: replace this install's files with the downloaded release and restart onto them. */
    async apply(): Promise<void> {
        if (this.busy) return;
        this.phase = 'applying';
        this.fault = '';
        try {
            this.lastApplied = await applyUpdate();
            if (this.lastApplied.restarting) return; // the process is going away; nothing left to read
            await this.refresh();
        } catch (e: any) {
            this.fault = messageOf(e);
            await this.refresh();
        }
        this.phase = 'idle';
    }

    /** *Restart to apply*. The server ends itself the way a signal would; this browser loses its socket. */
    async restart(): Promise<void> {
        if (this.busy) return;
        this.phase = 'restarting';
        this.fault = '';
        try {
            this.lastRestart = await restartToUpdate();
        } catch (e: any) {
            this.fault = messageOf(e);
            this.phase = 'idle';
        }
    }

    /**
     * The first-run question, once per browser: MIKE asks whether it may look for a newer release and,
     * finding one, download it. A write that failed still counts as asked — a prompt that returns on
     * every boot gets answered by accident.
     */
    async askConsent(): Promise<void> {
        if (!this.consentOwed) return;
        if (await readStamp(CONSENT_ASKED_KEY)) return;
        await saveAppSetting(CONSENT_ASKED_KEY, dayKey()).catch(() => {});
        const enabled = await dialogStore.showConfirm({
            title: t('updates.consent.title'),
            message: t('updates.consent.message'),
            confirmText: t('updates.consent.yes'),
            cancelText: t('updates.consent.no'),
        });
        await this.setConsent(enabled);
    }

    /** Write the answer to the preference. The About tab's toggle saves through Settings instead. */
    async setConsent(enabled: boolean): Promise<void> {
        try {
            await setUpdateCheckConsent(enabled);
            await this.refresh();
        } catch (e: any) {
            this.fault = messageOf(e);
        }
    }
}

function messageOf(e: any): string {
    return e?.message || String(e);
}

/** Read a stamp. Storage that will not answer reads as "not stamped", which asks at worst once more. */
async function readStamp(key: string): Promise<boolean> {
    try {
        return (await loadAppSetting<number>(key, 0)) !== 0;
    } catch {
        return false;
    }
}

async function stampToday(): Promise<boolean> {
    const today = dayKey();
    try {
        if ((await loadAppSetting<number>(LAST_CHECK_DAY_KEY, 0)) === today) return false;
        await saveAppSetting(LAST_CHECK_DAY_KEY, today);
        return true;
    } catch {
        // A store that will not write is a store that cannot remember it asked, so it does not ask.
        return false;
    }
}

/**
 * One stamp entry naming the release last fetched and the day it was fetched. Naming the version is what
 * makes the gate per release: a download that failed today is not retried every reload, and a newer
 * release appearing today is still fetched today.
 */
async function stampPrepare(version: string): Promise<boolean> {
    const stamp = `${version}:${dayKey()}`;
    try {
        if ((await loadAppSetting<string>(PREPARE_STAMP_KEY, '')) === stamp) return false;
        await saveAppSetting(PREPARE_STAMP_KEY, stamp);
        return true;
    } catch {
        return false;
    }
}

/** Compared, never shown — so no `Intl` and nothing locale-dependent at module scope. */
function dayKey(): number {
    const now = new Date();
    return (now.getFullYear() * 100 + now.getMonth() + 1) * 100 + now.getDate();
}

export const updateCheck = new UpdateCheckStore();
