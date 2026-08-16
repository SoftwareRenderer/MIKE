<script lang="ts">
    import type { Provider } from '$lib/types';
    import { appStore } from '$lib/stores';
    import { effectiveStartTaskKey, formatShortcut, shortcutHintsHidden } from '$lib/utils/keyboard';
    import ModelSelector from './ModelSelector.svelte';

    // Start Task shortcut hint (empty = disabled).
    const startShortcutLabel = $derived.by(() => {
        const key = effectiveStartTaskKey(appStore.config);
        return key ? formatShortcut(key) : '';
    });
    const hintsHidden = $derived(shortcutHintsHidden(appStore.config));

    let {
        providers,
        selectedModel,
        selectedUrl,
        selectedReasoningEffort,
        operatingMode,
        isStarting,
        disabled,
        isAgentSelected,
        onModelSelect,
        onOperatingModeToggle,
        onStartTask,
    }: {
        providers: Provider[];
        selectedModel: string;
        selectedUrl: string;
        selectedReasoningEffort: string;
        operatingMode: string;
        isStarting: boolean;
        disabled: boolean;
        isAgentSelected: () => boolean;
        onModelSelect: (providerName: string, modelName: string, providerUrl: string, reasoningEffort?: string) => void;
        onOperatingModeToggle: () => void;
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
            class:active={operatingMode === 'plan'}
            class:explore={operatingMode === 'explore'}
            onclick={onOperatingModeToggle}
            title={operatingMode === 'act' ? 'Act mode: AI is required to provide structured responses during review, and has read/write access. Click to switch.' : operatingMode === 'plan' ? 'Plan mode: AI decomposes work into subtasks on the board, and only has read access. Click to switch.' : 'Explore mode: AI has read/write access. Click to switch.'}
            aria-label="Toggle operating mode"
        >
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                {#if operatingMode === 'plan'}
                    <path d="M2 4h12M2 8h12M2 12h12" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                {:else if operatingMode === 'explore'}
                    <path d="M4 6a4 4 0 1 1 8 0c0 2.5-2 3.5-2 3.5v1h-4v-1s-2-1-2-3.5Z" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"/>
                    <path d="M6 10h4v1.5H6Z" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"/>
                    <path d="M6.5 12.5h3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                {:else}
                    <path d="M3 8l4 4L13 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                {/if}
            </svg>
            <span>{operatingMode === 'plan' ? 'Plan' : operatingMode === 'explore' ? 'Explore' : 'Act'}</span>
        </button>
        {/if}
    </div>
    <button class="btn-start-task" onclick={onStartTask} disabled={isStarting || disabled || (providers.length > 0 && !selectedModel)} title={startShortcutLabel ? `Start the AI task (${startShortcutLabel})` : 'Start the AI task'}>
        {#if isStarting}
            <span class="spinner-inline"></span> Starting...
        {:else}
            <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
                <path d="M4 2l10 6-10 6V2z" fill="currentColor"/>
            </svg>
            Start Task{#if startShortcutLabel && !hintsHidden}<kbd class="key-hint">{startShortcutLabel}</kbd>{/if}
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
        background: var(--color-surface-3);
        border: 1px solid var(--color-accent-light);
        color: var(--color-accent-light);
    }
    .btn-plan-toggle.explore {
        padding: 4px 10px;
        border: 1px solid var(--color-status-yellow);
        color: var(--color-status-yellow);
    }
    .btn-start-task {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        gap: 8px;
        padding: 10px 20px;
        border-radius: var(--radius-md);
        background: var(--color-accent-dark);
        color: var(--color-btn-text);
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
    .btn-start-task .key-hint {
        color: rgba(255, 255, 255, 0.85);
        border-color: rgba(255, 255, 255, 0.3);
        background: rgba(255, 255, 255, 0.1);
    }
    .spinner-inline {
        display: inline-block;
        width: 14px;
        height: 14px;
        border: 2px solid color-mix(in srgb, var(--color-btn-text) 30%, transparent);
        border-top-color: var(--color-btn-text);
        border-radius: 50%;
        animation: spin 0.6s linear infinite;
    }
    @keyframes spin {
        to { transform: rotate(360deg); }
    }
</style>
