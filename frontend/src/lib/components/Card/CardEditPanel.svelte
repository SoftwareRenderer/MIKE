<script lang="ts">
    import type { Provider } from '$lib/types';
    import ModelSelector from '../Chat/ModelSelector.svelte';
    import ToggleSwitch from '../ToggleSwitch.svelte';

    let {
        titleDraft,
        editProviders,
        editModel,
        editUrl,
        editReasoningEffort,
        editPlanMode,
        editMergeOnCommit,
        editMergeChildren,
        isAgentSelected,
        taskHasParent,
        targetBranch,
        titleInputRef,
        onSave,
        onCancel,
        onModelSelect,
    }: {
        titleDraft: string;
        editProviders: Provider[];
        editModel: string;
        editUrl: string;
        editReasoningEffort: string;
        editPlanMode: boolean;
        editMergeOnCommit: boolean;
        editMergeChildren: boolean;
        isAgentSelected: () => boolean;
        taskHasParent: boolean;
        targetBranch: string;
        titleInputRef: HTMLInputElement | null;
        onSave: (e: Event) => void;
        onCancel: (e: Event) => void;
        onModelSelect: (providerName: string, modelName: string, providerUrl: string, reasoningEffort?: string) => void;
    } = $props();
</script>

<div class="card-edit-panel" onclick={(e) => e.stopPropagation()} role="presentation">
    <div class="card-edit-header">
        <span class="card-edit-title">Edit Task</span>
        <button type="button" class="card-edit-close" onclick={(e) => { e.stopPropagation(); onCancel(e); }} aria-label="Close">✕</button>
    </div>
    <div class="card-edit-body">
        <label class="card-edit-field">
            <span class="card-edit-label">Title</span>
            <input
                class="card-edit-input"
                type="text"
                bind:value={titleDraft}
                onkeydown={(e) => { if (e.key === 'Enter') onSave(e); if (e.key === 'Escape') onCancel(e); e.stopPropagation(); }}
                oninput={(e) => e.stopPropagation()}
                bind:this={titleInputRef}
                placeholder="Task title"
                aria-label="Task title"
            />
        </label>
        {#if editProviders.length > 0}
            <div class="card-edit-model-row">
                <span class="card-edit-label">Model</span>
                <ModelSelector
                    providers={editProviders}
                    currentModel={editModel}
                    currentUrl={editUrl}
                    currentReasoningEffort={editReasoningEffort}
                    onSelect={onModelSelect}
                />
            </div>
        {/if}
        <div class="card-edit-options">
            {#if !isAgentSelected()}
            <div class="config-row">
                <div class="config-info">
                    <span class="config-label">Plan mode</span>
                    <span class="help-icon" title="AI decomposes work into subtasks on the board, then executes after approval">
                        <svg width="12" height="12" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                            <circle cx="8" cy="8" r="6.5" stroke="currentColor" stroke-width="1.2"/>
                            <text x="8" y="12" text-anchor="middle" fill="currentColor" font-size="0.75rem" font-weight="600">?</text>
                        </svg>
                    </span>
                </div>
                <ToggleSwitch bind:checked={editPlanMode} ariaLabel="Plan mode" />
            </div>
            {/if}
        </div>
        <div class="card-edit-section">
            <div class="section-label">Repository</div>
            <div class="config-row">
                <div class="config-info">
                    <span class="config-label">Auto-merge</span>
                    <span class="help-icon" title={taskHasParent ? 'Merge changes into parent task when complete' : `Merge changes into ${targetBranch} when complete`}>
                        <svg width="12" height="12" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                            <circle cx="8" cy="8" r="6.5" stroke="currentColor" stroke-width="1.2"/>
                            <text x="8" y="12" text-anchor="middle" fill="currentColor" font-size="0.75rem" font-weight="600">?</text>
                        </svg>
                    </span>
                </div>
                <ToggleSwitch bind:checked={editMergeOnCommit} ariaLabel="Auto-merge on completion" />
            </div>
            {#if !taskHasParent}
            <div class="config-row">
                <div class="config-info">
                    <span class="config-label">Allow subtasks to auto-merge</span>
                    <span class="help-icon" title="Subtasks created under this task will auto-merge into it">
                        <svg width="12" height="12" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                            <circle cx="8" cy="8" r="6.5" stroke="currentColor" stroke-width="1.2"/>
                            <text x="8" y="12" text-anchor="middle" fill="currentColor" font-size="0.75rem" font-weight="600">?</text>
                        </svg>
                    </span>
                </div>
                <ToggleSwitch bind:checked={editMergeChildren} ariaLabel="Allow subtasks to auto-merge" />
            </div>
            {/if}
        </div>
    </div>
    <div class="card-edit-footer">
        <button type="button" class="card-edit-btn-cancel" onclick={(e) => { e.stopPropagation(); onCancel(e); }}>Cancel</button>
        <button type="button" class="card-edit-btn-save" onclick={(e) => { e.stopPropagation(); onSave(e); }}>Save</button>
    </div>
</div>

<style>
    .card-edit-panel {
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        overflow: hidden;
    }
    .card-edit-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 8px 10px;
        border-bottom: 1px solid var(--color-border);
    }
    .card-edit-title {
        font-size: 0.8125rem;
        font-weight: 600;
        color: var(--color-text-primary);
    }
    .card-edit-close {
        font-size: 1rem;
        color: var(--color-text-secondary);
        background: none;
        border: none;
        cursor: pointer;
        padding: 2px 4px;
        line-height: 1;
    }
    .card-edit-close:hover {
        color: var(--color-text-primary);
    }
    .card-edit-body {
        padding: 10px;
        display: flex;
        flex-direction: column;
        gap: 10px;
    }
    .card-edit-field {
        display: flex;
        flex-direction: column;
        gap: 4px;
    }
    .card-edit-label {
        font-size: 0.8125rem;
        font-weight: 500;
        color: var(--color-text-secondary);
    }
    .card-edit-input {
        width: 100%;
        font-size: 0.8125rem;
        color: var(--color-text-primary);
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        padding: 6px 8px;
        font-family: inherit;
        outline: none;
    }
    .card-edit-input:focus {
        border-color: var(--color-accent);
    }
    .card-edit-model-row {
        display: flex;
        align-items: center;
        flex-wrap: wrap;
        gap: 8px;
    }
    .card-edit-options {
        display: flex;
        flex-direction: column;
        gap: 8px;
    }
    .card-edit-section {
        display: flex;
        flex-direction: column;
        gap: 8px;
        margin-top: 6px;
        padding-top: 8px;
        border-top: 1px solid var(--color-border);
    }
    .section-label {
        font-size: 0.75rem;
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
    .config-label {
        font-size: 0.8125rem;
        font-weight: 500;
        color: var(--color-text-primary);
    }
    .config-info {
        display: flex;
        align-items: center;
        gap: 6px;
    }
    .help-icon {
        display: flex;
        align-items: center;
        justify-content: center;
        color: var(--color-text-tertiary);
        cursor: help;
        flex-shrink: 0;
    }
    .help-icon:hover {
        color: var(--color-text-secondary);
    }
    .card-edit-footer {
        display: flex;
        justify-content: flex-end;
        gap: 8px;
        padding: 8px 10px;
        border-top: 1px solid var(--color-border);
        background: var(--color-surface-2);
    }
    .card-edit-btn-cancel {
        padding: 6px 14px;
        border-radius: var(--radius-sm);
        background: var(--color-surface-3);
        color: var(--color-text-primary);
        font-size: 0.8125rem;
        font-weight: 500;
        cursor: pointer;
        border: none;
        font-family: inherit;
    }
    .card-edit-btn-cancel:hover {
        background: var(--color-surface-4);
    }
    .card-edit-btn-save {
        padding: 6px 16px;
        border-radius: var(--radius-sm);
        background: var(--color-accent);
        color: #fff;
        font-size: 0.8125rem;
        font-weight: 500;
        cursor: pointer;
        border: none;
        font-family: inherit;
    }
    .card-edit-btn-save:hover {
        background: var(--color-accent-hover);
    }
</style>
