<script lang="ts">
    import { t, type MessageKey } from '$lib/i18n';
    import type { LockWait } from '$lib/types';

    interface Props {
        queuePosition: number;
        queueEstimatedWait: number;
        promptProgress: number;
        retryCount: number;
        retryError: string;
        isAgentSelected: () => boolean;
        /** Absent means no wait is known. */
        lockWait?: LockWait | null;
    }

    let { queuePosition, queueEstimatedWait, promptProgress, retryCount, retryError, isAgentSelected, lockWait = null }: Props = $props();

    // The held lock decides the sentence; an unlisted domain still gets one. A quiet provider is
    // keyed by phase as well: waiting for a first token and going silent mid-answer read differently.
    const LOCK_KEYS: Record<string, MessageKey> = {
        worktree: 'chat.queue.lockWorktree',
        stash: 'chat.queue.lockStash',
        'model:first_token': 'chat.queue.modelPrompt',
        'model:mid_response': 'chat.queue.modelQuiet',
    };

    let label = $derived(() => {
        if (lockWait) {
            const key = LOCK_KEYS[`${lockWait.domain}:${lockWait.phase}`] ?? LOCK_KEYS[lockWait.domain] ?? 'chat.queue.lockOther';
            return t(key, { seconds: lockWait.seconds });
        }
        const target = isAgentSelected() ? t('chat.queue.targetAgent') : t('chat.queue.targetLlm');
        if (queuePosition > 0) {
            return t('chat.queue.position', { target, position: queuePosition, wait: queueEstimatedWait });
        }
        if (retryCount > 0) {
            const errPart = retryError ? t('chat.queue.errorParen', { error: retryError }) : '';
            return t('chat.queue.connectionIssue', { errPart });
        }
        if (promptProgress > 0) {
            return t('chat.queue.promptProgress', { target, percent: promptProgress });
        }
        return t('chat.queue.awaiting', { target });
    });

    let variant = $derived(() => {
        if (retryCount > 0) return 'retry';
        return 'default';
    });
</script>

<div class="awaiting-llm" class:retry={variant() === 'retry'}>
    <span class="spinner"></span>
    <span>{label()}</span>
</div>

<style>
    .awaiting-llm {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 10px 16px;
        background: transparent;
        color: var(--color-text-secondary);
        font-size: 0.8rem;
    }
    .awaiting-llm.retry {
        color: var(--color-warning);
    }
    .spinner {
        display: inline-block;
        width: 12px;
        height: 12px;
        border: 2px solid currentColor;
        border-top-color: transparent;
        border-radius: 50%;
        animation: spin 0.6s linear infinite;
        flex-shrink: 0;
    }
    @keyframes spin {
        to { transform: rotate(360deg); }
    }
</style>
