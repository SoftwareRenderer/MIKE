<script lang="ts">
    import type { Config, Provider } from '$lib/types';
    import { AGENT_URL_PREFIX, isAgentProvider } from '$lib/types';
    import BaseModal from './BaseModal.svelte';
    import ModelSelector from './Chat/ModelSelector.svelte';
    import { resolveWorker, getModelName } from '$lib/utils/providers';
    import { t } from '$lib/i18n';

    let {
        providers,
        config,
        taskCount,
        taskTitles,
        onClose,
    }: {
        providers: Provider[];
        config: Config | null;
        taskCount: number;
        taskTitles: string[];
        onClose: () => void;
    } = $props();

    let selectedModel = $state('');
    let selectedUrl = $state('');
    let selectedReasoningEffort = $state('');
    let hasProviders = $derived(providers.length > 0);
    let isAgentSelected = $derived(() => {
        if (!selectedUrl) return false;
        if (selectedUrl.startsWith(AGENT_URL_PREFIX)) return true;
        const p = providers.find(p => p.url === selectedUrl);
        return p ? isAgentProvider(p) : false;
    });
    let modalTitle = $derived(t('chat.changeModel.title', { count: taskCount }));

    // Pre-select the configured Worker provider/model
    $effect(() => {
        if (providers.length > 0 && !selectedModel) {
            const worker = resolveWorker(config);
            if (worker.provider && worker.modelName) {
                selectedUrl = worker.url;
                selectedModel = worker.modelName;
            } else if ((providers[0].models?.length ?? 0) > 0) {
                selectedUrl = providers[0].url;
                selectedModel = getModelName(providers[0].models[0]);
            }
        }
    });

    function handleModelSelect(providerName: string, modelName: string, providerUrl: string, reasoningEffort?: string) {
        selectedModel = modelName;
        selectedUrl = providerUrl;
        selectedReasoningEffort = reasoningEffort || '';
    }

    function handleChange() {
        window.dispatchEvent(new CustomEvent('multi-select-model', {
            detail: {
                taskIds: [], // populated by the parent component's handler
                providerUrl: selectedUrl,
                modelName: selectedModel || undefined,
                reasoningEffort: selectedReasoningEffort || undefined,
            },
        }));
        onClose();
    }

    function handleClose() {
        onClose();
    }
</script>

{#snippet footer()}
    <button type="button" class="btn-secondary" onclick={handleClose} title={t('chat.common.cancel')}>{t('chat.common.cancel')}</button>
    <button type="button" class="btn-primary" disabled={!selectedModel} onclick={handleChange} title={t('chat.changeModel.confirmTip')}>
        {t('chat.changeModel.confirm')}
    </button>
{/snippet}

<BaseModal title={modalTitle} width="420px" footer={footer} onClose={handleClose} closeOnOverlayClick={true}>
    <div class="model-select-row">
        <span id="model-label" class="model-label">{t('chat.changeModel.model')}</span>
        {#if hasProviders}
            <ModelSelector
                providers={providers}
                currentModel={selectedModel}
                currentUrl={selectedUrl}
                currentReasoningEffort={selectedReasoningEffort}
                onSelect={handleModelSelect}
            />
        {:else}
            <span class="no-providers">{t('chat.changeModel.noProviders')}</span>
        {/if}
    </div>
    <ul class="task-list">
        {#each taskTitles as title}
            <li class="task-item">{title}</li>
        {/each}
    </ul>
</BaseModal>

<style>
    .task-list {
        list-style-type: disc !important;
        list-style-position: inside;
        margin: 8px 0 0;
        padding-left: 0;
        font-size: 0.8125rem;
        color: var(--color-text-secondary);
        line-height: 1.6;
        max-height: 180px;
        overflow-y: auto;
    }
    .task-item {
        display: list-item !important;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }
    .model-select-row {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 12px;
        padding: 12px 0;
        font-size: 0.875rem;
        font-weight: 500;
        color: var(--color-text-secondary);
    }
    .model-label {
        margin: 0;
    }
    .no-providers {
        font-size: 0.8125rem;
        color: var(--color-text-tertiary);
    }
</style>
