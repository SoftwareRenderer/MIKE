<script lang="ts">
    import type { Provider } from '$lib/types';
    import { PI_SENTINEL_URL, isAgentProvider } from '$lib/types';
    import BaseModal from './BaseModal.svelte';
    import ModelSelector from './Chat/ModelSelector.svelte';

    let {
        providers,
        taskCount,
        taskTitles,
        onClose,
    }: {
        providers: Provider[];
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
        if (selectedUrl === PI_SENTINEL_URL) return true;
        const p = providers.find(p => p.url === selectedUrl);
        return p ? isAgentProvider(p) : false;
    });
    let modalTitle = $derived(`Change Model for ${taskCount} task${taskCount !== 1 ? 's' : ''}`);

    // Pre-select first provider's default model
    $effect(() => {
        if (providers.length > 0 && !selectedModel) {
            const firstProvider = providers[0];
            const _getModelName = (m: string | { name: string; reasoning_effort?: string }) => typeof m === 'string' ? m : m.name;
            if (firstProvider.default_model && firstProvider.models.some(m => _getModelName(m) === firstProvider.default_model)) {
                selectedUrl = firstProvider.url;
                selectedModel = firstProvider.default_model;
            } else if (firstProvider.models.length > 0) {
                selectedUrl = firstProvider.url;
                selectedModel = _getModelName(firstProvider.models[0]);
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
    <button type="button" class="btn-secondary" onclick={handleClose} title="Cancel">Cancel</button>
    <button type="button" class="btn-primary" disabled={!selectedModel} onclick={handleChange} title="Change the model">
        Change Model
    </button>
{/snippet}

<BaseModal title={modalTitle} width="420px" footer={footer} onClose={handleClose} closeOnOverlayClick={true}>
    <div class="model-select-row">
        <span id="model-label" class="model-label">Model</span>
        {#if hasProviders}
            <ModelSelector
                providers={providers}
                currentModel={selectedModel}
                currentUrl={selectedUrl}
                currentReasoningEffort={selectedReasoningEffort}
                onSelect={handleModelSelect}
            />
        {:else}
            <span class="no-providers">No providers configured</span>
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
