<script lang="ts">
    import type { Provider } from '$lib/types';
    import { appStore } from '$lib/stores';
    import { effectiveStartTaskKey, formatShortcut, shortcutHintsHidden } from '$lib/utils/keyboard';
    import ModelSelector from './ModelSelector.svelte';
    import ModeToggle from './ModeToggle.svelte';
    import Spinner from '$lib/components/Spinner.svelte';
    import { t } from '$lib/i18n';

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
        <ModeToggle variant="panel" mode={operatingMode} onToggle={onOperatingModeToggle} />
        {/if}
    </div>
    <button class="btn-start-task" onclick={onStartTask} disabled={isStarting || (providers.length > 0 && !selectedModel)} title={startShortcutLabel ? t('chat.start.titleWithShortcut', { shortcut: startShortcutLabel }) : t('chat.start.title')}>
        {#if isStarting}
            <Spinner decorative /> {t('chat.start.starting')}
        {:else}
            <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
                <path d="M4 2l10 6-10 6V2z" fill="currentColor"/>
            </svg>
            {t('chat.start.button')}{#if startShortcutLabel && !hintsHidden}<kbd class="key-hint">{startShortcutLabel}</kbd>{/if}
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
    .btn-start-task {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        gap: 8px;
        padding: 10px 20px;
        border-radius: var(--radius-md);
        background: var(--color-accent-dark);
        color: var(--color-btn-text);
        font-size: var(--text-base);
        font-weight: 600;
        cursor: pointer;
        border: none;
        font-family: inherit;
        width: 100%;
    }
    .btn-start-task:hover:not(:disabled) {
        background: var(--color-accent-hover);
    }
    .btn-start-task .key-hint {
        color: rgba(255, 255, 255, 0.85);
        border-color: rgba(255, 255, 255, 0.3);
        background: rgba(255, 255, 255, 0.1);
    }
</style>
