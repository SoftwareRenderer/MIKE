<script lang="ts">
    import { t } from '$lib/i18n';

    interface Props {
        queuePosition: number;
        queueEstimatedWait: number;
        promptProgress: number;
        retryCount: number;
        retryError: string;
        isAgentSelected: () => boolean;
    }

    let { queuePosition, queueEstimatedWait, promptProgress, retryCount, retryError, isAgentSelected }: Props = $props();

    let label = $derived(() => {
        const target = isAgentSelected() ? t('chat.queue.targetAgent') : t('chat.queue.targetLlm');
        if (queuePosition > 0) {
            return t('chat.queue.position', { target, position: queuePosition, wait: queueEstimatedWait });
        }
        if (retryCount > 0) {
            const errPart = retryError ? t('chat.queue.errorParen', { error: retryError }) : '';
            return t('chat.queue.connectionIssue', { errPart, count: retryCount });
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
