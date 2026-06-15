<script lang="ts">
    import { onMount } from 'svelte';
    import { get } from 'svelte/store';
    import type { Provider } from '$lib/types';
    import { createTask, getTask } from '$lib/api';
    import { config as configStore, showCreateTask } from '$lib/store';
    import ModelSelector from './Chat/ModelSelector.svelte';
    import { createFocusTrap } from '$lib/a11y';

    let { projectId, onClose, onTaskCreated, parentId }: { projectId: string | undefined; onClose: () => void; onTaskCreated?: () => void; parentId?: string } = $props();

    let prompt = $state('');
    let autoCommit = $state(false);
    let autoPr = $state(false);
    let planMode = $state(false);
    let loading = $state(false);
    let error = $state('');
    let parentTitle = $state('');

    // Model selection state
    let providers: Provider[] = $state([]);
    let selectedModel = $state('');
    let selectedUrl = $state('');
    let selectedReasoningEffort = $state('');
    let hasProviders = $derived(providers.length > 0);

    onMount(async () => {
        triggerEl = document.activeElement as HTMLElement;
        // Use config from store (already fetched by App) instead of separate API call
        const cfg = get(configStore);
        if (cfg && cfg.providers) {
            providers = cfg.providers;
            // Pre-select highest-priority provider's default model
            if (providers.length > 0) {
                const firstProvider = providers[0];
                const _getModelName = (m: string | { name: string; reasoning_effort?: string }) => typeof m === 'string' ? m : m.name;
                if (firstProvider.default_model && firstProvider.models.some(m => _getModelName(m) === firstProvider.default_model)) {
                    selectedUrl = firstProvider.url;
                    selectedModel = firstProvider.default_model;
                } else if (firstProvider.models.length > 0) {
                    // Fallback: first available model of highest-priority provider
                    selectedUrl = firstProvider.url;
                    selectedModel = _getModelName(firstProvider.models[0]);
                }
            }
        }
        // Fetch parent task title if creating a subtask
        if (parentId && projectId) {
            try {
                const parentTask = await getTask(projectId, parentId);
                parentTitle = parentTask.title;
            } catch {
                parentTitle = 'Parent task';
            }
        }
        // Setup focus trap after modal is rendered
        if (modalEl) {
            trap = createFocusTrap(modalEl, handleClose);
            // Focus the description textarea
            setTimeout(() => {
                const textarea = modalEl?.querySelector('textarea') as HTMLElement;
                textarea?.focus();
            }, 50);
        }
    });

    function handleModelSelect(providerName: string, modelName: string, providerUrl: string, reasoningEffort?: string) {
        selectedModel = modelName;
        selectedUrl = providerUrl;
        selectedReasoningEffort = reasoningEffort || '';
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
            if (selectedReasoningEffort) taskData.reasoning_effort = selectedReasoningEffort;
            if (parentId) taskData.parent_task_id = parentId;
            await createTask(projectId, taskData);
            // Board stream WebSocket will push the update; onTaskCreated triggers refreshBoard as fallback
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
        trap?.destroy();
        trap = null;
        onClose();
        setTimeout(() => triggerEl?.focus(), 50);
    }

    let modalEl: HTMLDivElement | null = null;
    let trap: ReturnType<typeof createFocusTrap> | null = null;
    let triggerEl: HTMLElement | null = null;
</script>

<div class="overlay" onclick={handleClose} role="presentation">
    <div class="modal" onclick={(e) => e.stopPropagation()} onkeydown={(e) => { if (e.key === 'Escape') handleClose(); }} bind:this={modalEl} role="dialog" aria-modal="true" aria-labelledby="create-task-title" tabindex="-1">
        <div class="modal-header">
            <h2 id="create-task-title">{parentId ? 'Create Subtask' : 'Create Task'}</h2>
            <button class="close-btn" onclick={handleClose} aria-label="Close" title="Close">✕</button>
        </div>
        <form class="modal-body" onsubmit={(e) => { e.preventDefault(); handleSubmit(); }}>
            {#if parentId}
                <div class="parent-label">Subtask of: <strong>{parentTitle || 'Parent task'}</strong></div>
            {/if}
            <label for="task-description">
                Description
                <textarea id="task-description" bind:value={prompt} placeholder="Describe the task the AI should execute..." rows="5" required aria-invalid={!!error} aria-describedby={error ? 'task-error' : undefined}></textarea>
            </label>
            {#if hasProviders}
                <div class="model-select-row">
                    <span id="model-label" class="model-label" style="margin:0">Model</span>
                    <ModelSelector
                        providers={providers}
                        currentModel={selectedModel}
                        currentUrl={selectedUrl}
                        currentReasoningEffort={selectedReasoningEffort}
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
                <div id="task-error" class="error" role="alert">{error}</div>
            {/if}
            <div class="modal-footer">
                <button type="button" onclick={handleClose} title="Cancel">Cancel</button>
                <button type="submit" class="btn-primary" disabled={loading || !prompt.trim() || (hasProviders && !selectedModel)} title="Create the task">
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
        font-size: 1rem;
        font-weight: 600;
    }
    .close-btn {
        font-size: 1.125rem;
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
        font-size: 0.875rem;
        font-weight: 500;
        color: var(--color-text-secondary);
    }
    textarea {
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        padding: 8px 12px;
        color: var(--color-text-primary);
        font-size: 0.875rem;
    }
    textarea:focus-visible {
        border-color: var(--color-accent);
        outline: none;
    }
    textarea {
        resize: vertical;
        min-height: 80px;
    }
    .parent-label {
        font-size: 0.8125rem;
        color: var(--color-text-tertiary);
        padding: 6px 10px;
        background: var(--color-surface-2);
        border-radius: var(--radius-sm);
        border: 1px solid var(--color-border);
    }
    .parent-label strong {
        color: var(--color-text-secondary);
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
        font-size: 0.875rem;
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
        font-size: 0.875rem;
    }
    .option-desc {
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
        font-weight: 400;
    }
    .error {
        color: var(--color-status-red);
        font-size: 0.875rem;
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
            max-height: 100dvh;
            height: 100dvh;
            top: 0;
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