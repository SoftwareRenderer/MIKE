import { reloadExtensionVersion, setAgentMount } from '$lib/api';
import type { ExtensionConsentRequest } from '$lib/types';
import { t } from '$lib/i18n';

/** Public surface of one press of Try Extension. */
export interface TryExtension {
    /** A press is in flight; the button shows the spinner. */
    readonly busy: boolean;
    /** What the review bar says: the loaded sentence, the refusal's sentence, or nothing. */
    readonly notice: string;
    /** The version held for the person's answer, and null when nothing is being asked. */
    readonly consent: ExtensionConsentRequest | null;
    /** Loads this task's bytes; a refusal lands in `consent`, anything else in `notice`. */
    press(): Promise<void>;
    /** `mount` is the folder answer the install dialog gives: grant applies them, decline loads anyway. */
    answerConsent(mount: boolean): Promise<void>;
    /** Closing the dialog is a no: nothing is posted and the running version keeps running. */
    dismiss(): void;
}

/**
 * Try Extension loads a task's own bytes into the development row, and the manifest those bytes carry is
 * the one the agent wrote — so a version that reaches for the network or for a folder on this machine
 * comes back as a question instead (docs/extension-authoring.md → Loading). The server decides that
 * question and this holds the three states it needs: the press, the review-bar sentence, the pending
 * answer.
 */
export function useTryExtension(getExtensionId: () => string, getTaskId: () => string): TryExtension {
    let busy = $state(false);
    let notice = $state('');
    let consent = $state<ExtensionConsentRequest | null>(null);

    function asSentence(e: unknown): string {
        return e instanceof Error ? e.message : String(e);
    }

    async function pressWith(ack?: string) {
        const id = getExtensionId();
        if (!id) return;
        busy = true;
        try {
            const outcome = await reloadExtensionVersion(id, getTaskId(), ack);
            if (outcome.status === 'consent') {
                consent = outcome.consent;
                return;
            }
            // The bytes are loaded; nothing here remounts an extension's frame, so the person refreshes.
            notice = t('chat.review.tryExtensionLoaded');
        } catch (e) {
            notice = asSentence(e);
        } finally {
            busy = false;
        }
    }

    return {
        get busy() { return busy; },
        get notice() { return notice; },
        get consent() { return consent; },
        async press() {
            notice = '';
            await pressWith();
        },
        async answerConsent(mount: boolean) {
            const pending = consent;
            consent = null;
            if (!pending) return;
            if (mount && pending.agent_mounts?.length) {
                try {
                    await setAgentMount(getExtensionId(), pending.agent_mounts);
                } catch (e) {
                    notice = asSentence(e);
                    return;
                }
            }
            await pressWith(pending.fingerprint);
        },
        dismiss() { consent = null; },
    };
}
