<script lang="ts">
    
    import type { Provider } from '$lib/types';
    import ModelSelector from '../Chat/ModelSelector.svelte';
    import ToggleSwitch from '../ToggleSwitch.svelte';
    import BaseModal from '../BaseModal.svelte';

    let {
        taskTitle,
        taskPrompt,
        editProviders,
        editModel,
        editUrl,
        editReasoningEffort,
        editPlanMode,
        editAutoMerge,
        editSubtasksAutoMerge,
        taskHasParent,
        targetBranch,
        isAgentSelected,
        onSave,
        onCancel,
    }: {
        taskTitle: string;
        taskPrompt: string;
        editProviders: Provider[];
        editModel: string;
        editUrl: string;
        editReasoningEffort: string;
        editPlanMode: boolean;
        editAutoMerge: boolean;
        editSubtasksAutoMerge: boolean;
        taskHasParent: boolean;
        targetBranch: string;
        isAgentSelected: () => boolean;
        onSave: (title: string, model: string, url: string, reasoningEffort: string, planMode: boolean, autoMerge: boolean, subtasksAutoMerge: boolean) => void;
        onCancel: () => void;
    } = $props();

    let titleDraft = $state('');
    let modelDraft = $state('');
    let urlDraft = $state('');
    let effortDraft = $state('');
    let planModeDraft = $state(false);
    let autoMergeDraft = $state(true);
    let subtasksAutoMergeDraft = $state(true);

    $effect(() => { titleDraft = taskTitle; });
    $effect(() => { modelDraft = editModel; });
    $effect(() => { urlDraft = editUrl; });
    $effect(() => { effortDraft = editReasoningEffort; });
    $effect(() => { planModeDraft = editPlanMode; });
    $effect(() => { autoMergeDraft = editAutoMerge; });
    $effect(() => { subtasksAutoMergeDraft = editSubtasksAutoMerge; });

    let titleInput: HTMLInputElement | null = null;

    $effect(() => {
        setTimeout(() => {
            titleInput?.focus();
            titleInput?.select();
        }, 50);
    });

    function handleModelSelect(providerName: string, modelName: string, providerUrl: string, reasoningEffort?: string) {
        modelDraft = modelName;
        urlDraft = providerUrl;
        effortDraft = reasoningEffort || '';
    }

    function handleSave() {
        const trimmed = titleDraft.trim();
        if (!trimmed) return;
        onSave(trimmed, modelDraft, urlDraft, effortDraft, planModeDraft, autoMergeDraft, subtasksAutoMergeDraft);
    }

    function handleClose() {
        onCancel();
    }
</script>

<BaseModal title="Edit Task" width="440px" onClose={handleClose}>
            <label for="edit-task-title-input">
                <span class="field-label">Title</span>
                <input
                    id="edit-task-title-input"
                    bind:this={titleInput}
                    type="text"
                    bind:value={titleDraft}
                    onkeydown={(e) => { if (e.key === 'Enter') handleSave(); }}
                    placeholder="Task title"
                />
            </label>
            {#if editProviders.length > 0}
                <div class="model-select-row">
                    <span class="field-label">Model</span>
                    <ModelSelector
                        providers={editProviders}
                        currentModel={modelDraft}
                        currentUrl={urlDraft}
                        currentReasoningEffort={effortDraft}
                        onSelect={handleModelSelect}
                    />
                </div>
            {/if}
            {#if !isAgentSelected()}
            <div class="options">
                <div class="config-row">
                    <div class="config-info">
                        <span class="config-label">Plan mode</span>
                        <span class="config-desc">AI decomposes work into subtasks on the board, then executes after approval</span>
                    </div>
                    <ToggleSwitch bind:checked={planModeDraft} ariaLabel="Plan mode" />
                </div>
            </div>
            {/if}
            <div class="options-section">
                <div class="section-label">Repository</div>
                <div class="config-row">
                    <div class="config-info">
                        <span class="config-label">Auto-merge</span>
                        <span class="config-desc">{taskHasParent ? 'Merge changes into parent task when complete' : `Merge changes into ${targetBranch} when complete`}</span>
                    </div>
                    <ToggleSwitch bind:checked={autoMergeDraft} ariaLabel="Auto-merge on completion" />
                </div>
                {#if !taskHasParent}
                <div class="config-row">
                    <div class="config-info">
                        <span class="config-label">Allow subtasks to auto-merge</span>
                        <span class="config-desc">Subtasks created under this task will auto-merge into it</span>
                    </div>
                    <ToggleSwitch bind:checked={subtasksAutoMergeDraft} ariaLabel="Allow subtasks to auto-merge" />
                </div>
                {/if}
            </div>
            <div slot="footer" class="footer-wrapper">
                <button type="button" class="btn-secondary" onclick={handleClose}>Cancel</button>
                <button type="button" class="btn-primary" onclick={handleSave} disabled={!titleDraft.trim()}>Save</button>
            </div>
</BaseModal>

<style>
    :global(.modal) {
        box-shadow: 0 12px 40px rgba(0,0,0,0.3);
    }
    :global(.modal-header) {
        padding: 14px 18px;
    }
    :global(.modal-body) {
        padding: 16px 18px 18px;
        display: flex;
        flex-direction: column;
        gap: 14px;
    }
    .footer-wrapper {
        display: contents;
    }
    label {
        display: flex;
        flex-direction: column;
        gap: 5px;
    }
    .field-label {
        font-size: 0.8125rem;
        font-weight: 500;
        color: var(--color-text-secondary);
    }
    input[type="text"] {
        width: 100%;
        font-size: 0.875rem;
        color: var(--color-text-primary);
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        padding: 8px 10px;
        font-family: inherit;
        outline: none;
    }
    input[type="text"]:focus {
        border-color: var(--color-accent);
    }
    .model-select-row {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 12px;
    }
    .options {
        display: flex;
        flex-direction: column;
        gap: 8px;
    }
    .options-section {
        display: flex;
        flex-direction: column;
        gap: 8px;
        padding-top: 10px;
        border-top: 1px solid var(--color-border);
    }
    .section-label {
        font-size: 0.6875rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.05em;
        color: var(--color-text-tertiary);
    }
    .config-row {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 12px;
    }
    .config-info {
        display: flex;
        flex-direction: column;
        gap: 1px;
        min-width: 0;
    }
    .config-label {
        font-size: 0.8125rem;
        font-weight: 500;
        color: var(--color-text-primary);
    }
    .config-desc {
        font-size: 0.6875rem;
        color: var(--color-text-tertiary);
    }
    @media (max-width: 768px) {
        :global(.modal-body) {
            padding: 16px;
        }
    }
</style>
