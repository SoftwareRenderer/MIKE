<script lang="ts">
    import type { Provider } from '$lib/types';
    import ModelSelector from './ModelSelector.svelte';

    let {
        providers,
        selectedModel,
        selectedUrl,
        selectedReasoningEffort,
        planMode,
        isStarting,
        disabled,
        isAgentSelected,
        onModelSelect,
        onPlanModeToggle,
        onStartTask,
    }: {
        providers: Provider[];
        selectedModel: string;
        selectedUrl: string;
        selectedReasoningEffort: string;
        planMode: boolean;
        isStarting: boolean;
        disabled: boolean;
        isAgentSelected: () => boolean;
        onModelSelect: (providerName: string, modelName: string, providerUrl: string, reasoningEffort?: string) => void;
        onPlanModeToggle: (value: boolean) => void;
        onStartTask: () => void;
    } = $props();
</script>

<div class="chat-start">
    <div class="start-model-row">
        {#if providers.length > 0}
            <ModelSelector
                providers={providers}
                currentModel={selectedModel}
                currentUrl={selectedUrl}
                currentReasoningEffort={selectedReasoningEffort}
                onSelect={onModelSelect}
            />
        {/if}
        {#if !isAgentSelected()}
        <button
            type="button"
            class="btn-plan-toggle"
            class:active={planMode}
            onclick={() => onPlanModeToggle(!planMode)}
            title={planMode ? 'Plan mode: AI proposes subtasks. Click to switch to act mode.' : 'Act mode: AI makes changes directly. Click to switch to plan mode.'}
            aria-label="Toggle plan mode"
        >
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                {#if planMode}
                    <path d="M2 4h12M2 8h12M2 12h12" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                {:else}
                    <path d="M3 8l4 4L13 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                {/if}
            </svg>
            <span>{planMode ? 'Plan' : 'Act'}</span>
        </button>
        {/if}
    </div>
    <button class="btn-start-task" onclick={onStartTask} disabled={isStarting || disabled || (providers.length > 0 && !selectedModel)} title="Start the AI task">
        {#if isStarting}
            <span class="spinner-inline"></span> Starting...
        {:else}
            <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
                <path d="M4 2l10 6-10 6V2z" fill="currentColor"/>
            </svg>
            Start Task
        {/if}
    </button>
</div>

<style>
    .chat-start {
        display: flex;
        flex-direction: column;
        gap: 12px;
        padding: 16px;
        background: var(--color-surface-2);
        border-radius: var(--radius-md);
        margin: 0 12px;
    }
    .start-model-row {
        display: flex;
        align-items: center;
        gap: 12px;
    }
    .btn-plan-toggle {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        padding: 6px 12px;
        border-radius: var(--radius-sm);
        background: var(--color-surface-3);
        color: var(--color-text-primary);
        font-size: 0.8125rem;
        font-weight: 500;
        cursor: pointer;
        border: none;
        font-family: inherit;
    }
    .btn-plan-toggle:hover {
        background: var(--color-surface-4);
    }
    .btn-plan-toggle.active {
        padding: 4px 10px;
        border: 2px dotted var(--color-accent);
        color: var(--color-accent);
    }
    .btn-start-task {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        gap: 8px;
        padding: 10px 20px;
        border-radius: var(--radius-md);
        background: var(--color-accent);
        color: #fff;
        font-size: 0.875rem;
        font-weight: 600;
        cursor: pointer;
        border: none;
        font-family: inherit;
        width: 100%;
    }
    .btn-start-task:hover:not(:disabled) {
        background: var(--color-accent-hover);
    }
    .btn-start-task:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }
    .spinner-inline {
        display: inline-block;
        width: 14px;
        height: 14px;
        border: 2px solid rgba(255,255,255,0.3);
        border-top-color: #fff;
        border-radius: 50%;
        animation: spin 0.6s linear infinite;
    }
    @keyframes spin {
        to { transform: rotate(360deg); }
    }
</style>
