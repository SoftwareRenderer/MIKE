<script lang="ts">
    interface Props {
        queuePosition: number;
        queueEstimatedWait: number;
        promptProgress: number;
        isAgentSelected: () => boolean;
    }

    let { queuePosition, queueEstimatedWait, promptProgress, isAgentSelected }: Props = $props();

    let label = $derived(() => {
        const target = isAgentSelected() ? 'agent' : 'LLM provider';
        if (queuePosition > 0) {
            return `Awaiting ${target} — position ${queuePosition} in queue, ~${queueEstimatedWait}s`;
        }
        if (promptProgress > 0) {
            return `Awaiting ${target}... Processing Prompt: ${promptProgress}%`;
        }
        return `Awaiting ${target}...`;
    });
</script>

<div class="awaiting-llm">
    <span class="spinner"></span>
    <span>{label()}</span>
</div>

<style>
    .awaiting-llm {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 10px 16px;
        color: var(--color-text-secondary);
        font-size: 0.8rem;
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
