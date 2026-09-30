<script lang="ts">
    import { t, type MessageKey } from '$lib/i18n';
    import type { LockWait } from '$lib/types';
    import { prioritizeTask } from '$lib/api';
    import Spinner from '$lib/components/Spinner.svelte';

    interface Props {
        queuePosition: number;
        queueEstimatedWait: number;
        promptProgress: number;
        retryCount: number;
        retryError: string;
        isAgentSelected: () => boolean;
        /** Absent means no wait is known. */
        lockWait?: LockWait | null;
        /** True while the wait is for a provider running-task slot — the one queue a click reorders. */
        canPrioritize?: boolean;
        projectId?: string;
        taskId?: string;
    }

    let {
        queuePosition,
        queueEstimatedWait,
        promptProgress,
        retryCount,
        retryError,
        isAgentSelected,
        lockWait = null,
        canPrioritize = false,
        projectId = '',
        taskId = '',
    }: Props = $props();

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

    let asked = $state(false);
    let sending = $state(false);

    // The confirmation is one wait's, not the run's: the position going away means this wait is over, and
    // the next one has to be able to ask again.
    $effect(() => {
        if (queuePosition === 0) asked = false;
    });

    async function prioritize(): Promise<void> {
        // A second click can land before the button has had its `disabled` drawn.
        if (sending) return;
        sending = true;
        try {
            // Only a queue the server found this task in counts as answered. A refused or failed click
            // leaves the button standing: the position this stream reports every second is the feedback.
            asked = await prioritizeTask(projectId, taskId);
        } catch {
            asked = false;
        } finally {
            sending = false;
        }
    }
</script>

<div class="awaiting-llm" class:retry={variant() === 'retry'}>
    <Spinner size={12} decorative />
    <span>{label()}</span>
    {#if canPrioritize && projectId && taskId}
        {#if asked}
            <span class="prioritized" role="status">{t('chat.queue.prioritized')}</span>
        {:else}
            <button type="button" class="prioritize" onclick={prioritize} disabled={sending}>
                {t('chat.queue.prioritize')}
            </button>
        {/if}
    {/if}
</div>

<style>
    .awaiting-llm {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 10px 16px;
        background: transparent;
        color: var(--color-text-secondary);
        font-size: var(--text-sm);
    }
    .awaiting-llm.retry {
        color: var(--color-warning);
    }
    .prioritize {
        padding: 2px 8px;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        background: var(--color-surface-1);
        color: var(--color-text-secondary);
        font-size: var(--text-xs);
        cursor: pointer;
    }
    .prioritize:hover:not(:disabled) {
        border-color: var(--color-accent);
        color: var(--color-text-primary);
    }
    .prioritize:disabled {
        cursor: default;
        opacity: var(--opacity-disabled);
    }
    .prioritized {
        color: var(--color-text-tertiary);
        font-size: var(--text-xs);
    }
</style>
