<script lang="ts">
    import { onMount } from 'svelte';
    import type { Provider } from '$lib/types';
    import { AGENT_URL_PREFIX, isAgentProvider } from '$lib/types';
    import { appStore } from '$lib/stores';
    import { getLocalBranches } from '$lib/api';
    import { MODE_DESCRIPTIONS } from '$lib/constants/modes';
    import ModelSelector from '../Chat/ModelSelector.svelte';
    import ToggleSwitch from '../ToggleSwitch.svelte';
    import ModeSelect from '../ModeSelect.svelte';
    import BranchPicker from '../BranchPicker.svelte';
    import BaseModal from '../BaseModal.svelte';
    import { t } from '$lib/i18n';

    let {
        taskId,
        projectId,
        taskTitle,
        editProviders,
        editModel,
        editUrl,
        editReasoningEffort,
        editOperatingMode,
        editAutoMerge,
        editSubtasksAutoMerge,
        taskHasParent,
        targetBranch,
        baseRef,
        onSave,
        onCancel,
    }: {
        taskId: string;
        projectId: string;
        taskTitle: string;
        editProviders: Provider[];
        editModel: string;
        editUrl: string;
        editReasoningEffort: string;
        editOperatingMode: string;
        editAutoMerge: boolean;
        editSubtasksAutoMerge: boolean;
        taskHasParent: boolean;
        targetBranch: string;
        baseRef: string;
        onSave: (title: string, model: string, url: string, reasoningEffort: string, operatingMode: string, autoMerge: boolean, subtasksAutoMerge: boolean, baseRef: string) => void;
        onCancel: () => void;
    } = $props();

    let titleDraft = $state('');
    let modelDraft = $state('');
    let urlDraft = $state('');
    let effortDraft = $state('');
    let operatingModeDraft = $state('act');
    let autoMergeDraft = $state(true);
    let subtasksAutoMergeDraft = $state(true);
    let branchDraft = $state('');

    // Branch list for selector
    let branches = $state<string[]>([]);

    // Look up the live task from the board so auto-merge values stay in sync
    // when the server updates them (e.g., parent subtasks_auto_merge propagation).
    const liveTask = $derived.by(() => {
        return appStore.board?.tasks.find(t => t.id === taskId) ?? null;
    });

    // Track whether the user has manually toggled each switch.
    // Once touched, live-sync skips the field so local edits take priority.
    let userTouchedAutoMerge = $state(false);
    let userTouchedSubtasksAutoMerge = $state(false);

    $effect(() => { titleDraft = taskTitle; });
    $effect(() => { modelDraft = editModel; });
    $effect(() => { urlDraft = editUrl; });
    $effect(() => { effortDraft = editReasoningEffort; });
    $effect(() => { operatingModeDraft = editOperatingMode; });
    $effect(() => { autoMergeDraft = editAutoMerge; });
    $effect(() => { subtasksAutoMergeDraft = editSubtasksAutoMerge; });
    $effect(() => { branchDraft = baseRef || targetBranch; });

    // Live-sync auto-merge values from the board (e.g., after parent propagation).
    // Skips fields the user has already toggled so local edits are preserved.
    $effect(() => {
        const val = liveTask?.auto_merge;
        if (val !== undefined && !userTouchedAutoMerge) {
            autoMergeDraft = val;
        }
    });
    $effect(() => {
        const val = liveTask?.subtasks_auto_merge;
        if (val !== undefined && !userTouchedSubtasksAutoMerge) {
            subtasksAutoMergeDraft = val;
        }
    });

    let titleInput: HTMLInputElement | null = null;
    // Track the initial operating mode so we can restore it when switching away from agent
    let initialOperatingMode = $state('act');

    $effect(() => {
        setTimeout(() => {
            titleInput?.focus();
            titleInput?.select();
        }, 50);
    });

    onMount(async () => {
        initialOperatingMode = editOperatingMode;
        if (taskHasParent || !projectId) return;
        try {
            branches = await getLocalBranches(projectId);
        } catch {
            // Ignore errors
        }
    });

    function handleModelSelect(_providerName: string, modelName: string, providerUrl: string, reasoningEffort?: string) {
        modelDraft = modelName;
        urlDraft = providerUrl;
        effortDraft = reasoningEffort || '';
        // Agent providers always use explore mode
        const provider = editProviders.find(p => p.url === providerUrl);
        if (providerUrl.startsWith(AGENT_URL_PREFIX) || (provider && isAgentProvider(provider))) {
            operatingModeDraft = 'explore';
        } else if (operatingModeDraft === 'explore') {
            // Switching away from agent: restore the original mode
            operatingModeDraft = initialOperatingMode;
        }
    }

    function handleSave() {
        const trimmed = titleDraft.trim();
        if (!trimmed) return;
        onSave(trimmed, modelDraft, urlDraft, effortDraft, operatingModeDraft, autoMergeDraft, subtasksAutoMergeDraft, branchDraft.trim() || targetBranch);
    }

    function handleClose() {
        onCancel();
    }

    const modeDescription = $derived(MODE_DESCRIPTIONS[operatingModeDraft] || '');

    // Check if current draft URL is an agent provider (updates when user switches providers)
    const isAgentDraft = $derived.by(() => {
        if (!urlDraft) return false;
        if (urlDraft.startsWith(AGENT_URL_PREFIX)) return true;
        const p = editProviders.find(p => p.url === urlDraft);
        return p ? isAgentProvider(p) : false;
    });
</script>

{#snippet footer()}
            <button type="button" class="btn" onclick={handleClose}>{t('board.confirm.cancel')}</button>
            <button type="button" class="btn btn-primary" onclick={handleSave} disabled={!titleDraft.trim()}>{t('board.editTask.save')}</button>
        {/snippet}

        <BaseModal title={t('board.editTask.title')} width="440px" footer={footer} onClose={handleClose} closeOnOverlayClick={false}>
            <label for="edit-task-title-input">
                <span class="field-label">{t('board.editTask.fieldTitle')}</span>
                <input
                    id="edit-task-title-input"
                    bind:this={titleInput}
                    type="text"
                    bind:value={titleDraft}
                    onkeydown={(e) => { if (e.key === 'Enter') handleSave(); }}
                    placeholder={t('board.editTask.titlePlaceholder')}
                />
            </label>
            {#if editProviders.length > 0}
                <div class="model-select-row">
                    <span class="field-label">{t('board.editTask.fieldModel')}</span>
                    <ModelSelector
                        providers={editProviders}
                        currentModel={modelDraft}
                        currentUrl={urlDraft}
                        currentReasoningEffort={effortDraft}
                        onSelect={handleModelSelect}
                    />
                </div>
            {/if}
            {#if !isAgentDraft}
            <div class="options">
                <div class="config-row">
                    <div class="config-info">
                        <span class="config-label">{t('board.editTask.operatingMode')}</span>
                        <span class="config-desc">{modeDescription}</span>
                    </div>
                    <ModeSelect value={operatingModeDraft} onChange={(m: string) => operatingModeDraft = m} />
                </div>
            </div>
            {/if}
            <div class="options-section">
                <div class="section-label">{t('board.editTask.repository')}</div>
                {#if !taskHasParent}
                <div class="config-row">
                    <div class="config-info">
                        <span class="config-label">{t('board.editTask.branch')}</span>
                        <span class="config-desc">{t('board.editTask.branchDesc')}</span>
                    </div>
                    <BranchPicker bind:value={branchDraft} {branches} placeholder={targetBranch} />
                </div>
                {/if}
                <div class="config-row">
                    <div class="config-info">
                        <span class="config-label">{t('board.editTask.autoMerge')}</span>
                        <span class="config-desc">{taskHasParent ? t('board.editTask.autoMergeIntoParent') : t('board.editTask.autoMergeInto', { branch: branchDraft || targetBranch })}</span>
                    </div>
                    <ToggleSwitch bind:checked={autoMergeDraft} onChange={() => { userTouchedAutoMerge = true; }} ariaLabel="Auto-merge on completion" />
                </div>
                {#if !taskHasParent}
                <div class="config-row">
                    <div class="config-info">
                        <span class="config-label">{t('board.editTask.subtasksAutoMerge')}</span>
                        <span class="config-desc">{t('board.editTask.subtasksAutoMergeDesc')}</span>
                    </div>
                    <ToggleSwitch bind:checked={subtasksAutoMergeDraft} onChange={() => { userTouchedSubtasksAutoMerge = true; }} ariaLabel="Allow subtasks to auto-merge" />
                </div>
                {/if}
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
    label {
        display: flex;
        flex-direction: column;
        gap: 5px;
    }
    .field-label {
        font-size: var(--text-sm);
        font-weight: 500;
        color: var(--color-text-secondary);
    }
    input[type="text"] {
        width: 100%;
        font-size: var(--text-base);
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
        font-size: var(--text-2xs);
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
        font-size: var(--text-sm);
        font-weight: 500;
        color: var(--color-text-primary);
    }
    .config-desc {
        font-size: var(--text-2xs);
        color: var(--color-text-tertiary);
    }
    @media (max-width: 768px) {
        :global(.modal-body) {
            padding: 16px;
        }
    }
</style>
