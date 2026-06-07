<script lang="ts">
    import { onMount } from 'svelte';
    import { get } from 'svelte/store';
    import type { Provider } from '$lib/types';
    import { createTask, getBoard, getConfig } from '$lib/api';
    import { board as boardStore, showCreateTask } from '$lib/store';
    import ModelSelector from './Chat/ModelSelector.svelte';

    let { projectId, onClose, onTaskCreated }: { projectId: string | undefined; onClose: () => void; onTaskCreated?: () => void } = $props();

    let prompt = $state('');
    let autoCommit = $state(false);
    let autoPr = $state(false);
    let planMode = $state(false);
    let loading = $state(false);
    let error = $state('');

    // Model selection state
    let providers: Provider[] = $state([]);
    let selectedModel = $state('');
    let selectedUrl = $state('');
    let hasProviders = $derived(providers.length > 0);

    onMount(async () => {
        try {
            const cfg = await getConfig();
            providers = cfg.providers || [];
            // Pre-select highest-priority provider's default model
            if (providers.length > 0) {
                const firstProvider = providers[0];
                if (firstProvider.default_model && firstProvider.models.includes(firstProvider.default_model)) {
                    selectedUrl = firstProvider.url;
                    selectedModel = firstProvider.default_model;
                } else if (firstProvider.models.length > 0) {
                    // Fallback: first available model of highest-priority provider
                    selectedUrl = firstProvider.url;
                    selectedModel = firstProvider.models[0];
                }
            }
        } catch {
            providers = [];
        }
    });

    function handleModelSelect(providerName: string, modelName: string, providerUrl: string) {
        selectedModel = modelName;
        selectedUrl = providerUrl;
    }

    async function handleSubmit() {
        if (!projectId || !prompt.trim()) return;
        loading = true;
        error = '';
        try {
            const taskData: Record<string, unknown> = {
                prompt: prompt.trim(),
                auto_commit: autoCommit,
                auto_pr: autoPr,
                plan_mode: planMode,
            };
            if (selectedModel) taskData.model_name = selectedModel;
            if (selectedUrl) taskData.llama_server_url = selectedUrl;
            await createTask(projectId, taskData);
            // Refresh the board
            const board = await getBoard(projectId);
            boardStore.set(board);
            showCreateTask.set(false);
            onTaskCreated?.();
            onClose();
        } catch (e) {
            error = String(e);
        } finally {
            loading = false;
        }
    }

    function handleClose() {
        onClose();
    }
</script>

<div class="overlay" onclick={handleClose}>
    <div class="modal" onclick={(e) => e.stopPropagation()}>
        <div class="modal-header">
            <h2>Create Task</h2>
            <button class="close-btn" onclick={handleClose}>✕</button>
        </div>
        <form class="modal-body" onsubmit={(e) => { e.preventDefault(); handleSubmit(); }}>
            <label>
                Description
                <textarea bind:value={prompt} placeholder="Describe the task the AI should execute..." rows="5" required></textarea>
            </label>
            {#if hasProviders}
                <div class="model-select-row">
                    <label style="margin:0">Model</label>
                    <ModelSelector
                        providers={providers}
                        currentModel={selectedModel}
                        currentUrl={selectedUrl}
                        onSelect={handleModelSelect}
                    />
                </div>
            {/if}
            <div class="options">
                <label class="checkbox-label">
                    <input type="checkbox" bind:checked={planMode} />
                    <span class="option-text">
                        <span class="option-title">Plan mode</span>
                        <span class="option-desc">AI creates a TODO.md plan first, then executes tasks after approval</span>
                    </span>
                </label>
                <label class="checkbox-label">
                    <input type="checkbox" bind:checked={autoCommit} />
                    Auto-commit on completion
                </label>
                <label class="checkbox-label" class:disabled={!autoCommit}>
                    <input type="checkbox" bind:checked={autoPr} disabled={!autoCommit} />
                    Auto-create PR branch
                </label>
            </div>
            {#if error}
                <div class="error">{error}</div>
            {/if}
            <div class="modal-footer">
                <button type="button" onclick={handleClose}>Cancel</button>
                <button type="submit" class="btn-primary" disabled={loading || !prompt.trim() || (hasProviders && !selectedModel)}>
                    {loading ? 'Creating...' : 'Create Task'}
                </button>
            </div>
        </form>
    </div>
</div>

<style>
    .overlay {
        position: fixed;
        inset: 0;
        background: rgba(0,0,0,0.6);
        display: flex;
        align-items: center;
        justify-content: center;
        z-index: 100;
    }
    .modal {
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-lg);
        width: 480px;
        max-width: 90vw;
        max-height: 90vh;
        overflow-y: auto;
    }
    .modal-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 16px 20px;
        border-bottom: 1px solid var(--color-border);
    }
    .modal-header h2 {
        font-size: 16px;
        font-weight: 600;
    }
    .close-btn {
        font-size: 18px;
        color: var(--color-text-secondary);
        padding: 4px;
    }
    .modal-body {
        padding: 20px;
        display: flex;
        flex-direction: column;
        gap: 16px;
    }
    label {
        display: flex;
        flex-direction: column;
        gap: 6px;
        font-size: 13px;
        font-weight: 500;
        color: var(--color-text-secondary);
    }
    input[type="text"], textarea {
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        padding: 8px 12px;
        color: var(--color-text-primary);
        font-size: 14px;
        outline: none;
    }
    input[type="text"]:focus, textarea:focus {
        border-color: var(--color-accent);
    }
    textarea {
        resize: vertical;
        min-height: 80px;
    }
    .options {
        display: flex;
        flex-direction: column;
        gap: 8px;
    }
    .model-select-row {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 12px;
        font-size: 13px;
        font-weight: 500;
        color: var(--color-text-secondary);
    }
    .checkbox-label {
        flex-direction: row !important;
        align-items: center;
        gap: 8px !important;
        cursor: pointer;
    }
    .checkbox-label.disabled {
        opacity: 0.5;
        pointer-events: none;
    }
    .option-text {
        display: flex;
        flex-direction: column;
        align-items: flex-start;
        gap: 2px;
    }
    .option-title {
        font-weight: 600;
        font-size: 13px;
    }
    .option-desc {
        font-size: 11px;
        color: var(--color-text-tertiary);
        font-weight: 400;
    }
    .error {
        color: var(--color-status-red);
        font-size: 13px;
    }
    .modal-footer {
        display: flex;
        justify-content: flex-end;
        gap: 8px;
        padding-top: 8px;
        border-top: 1px solid var(--color-border);
    }
    .btn-primary {
        padding: 8px 20px;
        border-radius: var(--radius-md);
        background: var(--color-accent);
        color: white;
        font-weight: 500;
    }
    .btn-primary:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }
    button[type="button"] {
        padding: 8px 16px;
        border-radius: var(--radius-md);
        background: var(--color-surface-3);
    }

    @media (max-width: 768px) {
        .modal {
            width: 100vw;
            max-width: none;
            max-height: 100vh;
            height: 100vh;
            border-radius: 0;
            border: none;
        }
        .modal-header {
            padding: 16px;
        }
        .modal-body {
            padding: 16px;
        }
        button[type="button"],
        .btn-primary {
            min-height: 44px;
        }
    }
</style>