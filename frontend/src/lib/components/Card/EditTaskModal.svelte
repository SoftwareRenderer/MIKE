<script lang="ts">
    import { onMount } from 'svelte';
    import type { Provider } from '$lib/types';
    import { AGENT_URL_PREFIX, isAgentProvider } from '$lib/types';
    import { appStore } from '$lib/stores';
    import { MODE_DESCRIPTIONS } from '$lib/constants/modes';
    import ModelSelector from '../Chat/ModelSelector.svelte';
    import ToggleSwitch from '../ToggleSwitch.svelte';
    import ModeSelect from '../ModeSelect.svelte';
    import BaseModal from '../BaseModal.svelte';

    let {
        taskId,
        projectId,
        taskTitle,
        taskPrompt,
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
        isAgentSelected,
        onSave,
        onCancel,
    }: {
        taskId: string;
        projectId: string;
        taskTitle: string;
        taskPrompt: string;
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
        isAgentSelected: () => boolean;
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
    // svelte-ignore non_reactive_update
    let branchInput: HTMLInputElement | null = null;
    let branchDropdownOpen = $state(false);
    let branchActiveIndex = $state(0);
    // svelte-ignore non_reactive_update
    let branchDropdownRef: HTMLUListElement | null = null;

    // Filtered branches matching the current input text
    let filteredBranches = $derived.by(() => {
        const query = branchDraft.trim().toLowerCase();
        if (!query) return branches;
        return branches.filter(b => b.toLowerCase().includes(query));
    });

    function openBranchDropdown() {
        branchDropdownOpen = true;
        branchActiveIndex = 0;
    }

    function closeBranchDropdown() {
        branchDropdownOpen = false;
        branchActiveIndex = 0;
    }

    function handleBranchInput() {
        openBranchDropdown();
    }

    function handleBranchKeydown(e: KeyboardEvent) {
        if (!branchDropdownOpen || filteredBranches.length === 0) return;
        if (e.key === 'ArrowDown') {
            e.preventDefault();
            branchActiveIndex = (branchActiveIndex + 1) % filteredBranches.length;
        } else if (e.key === 'ArrowUp') {
            e.preventDefault();
            branchActiveIndex = (branchActiveIndex - 1 + filteredBranches.length) % filteredBranches.length;
        } else if (e.key === 'Enter' && branchActiveIndex >= 0 && branchActiveIndex < filteredBranches.length) {
            e.preventDefault();
            branchDraft = filteredBranches[branchActiveIndex];
            closeBranchDropdown();
        } else if (e.key === 'Escape') {
            closeBranchDropdown();
        }
    }

    function selectBranch(branch: string) {
        branchDraft = branch;
        closeBranchDropdown();
    }

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
            const res = await fetch(`/api/projects/${projectId}/git/branches`);
            if (res.ok) {
                const data: string[] = await res.json();
                // Filter to local branches only (no origin/ prefix)
                branches = data.filter(b => !b.startsWith('origin/') && b !== 'HEAD');
            }
        } catch {
            // Ignore errors
        }
    });

    function handleModelSelect(providerName: string, modelName: string, providerUrl: string, reasoningEffort?: string) {
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
            <button type="button" class="btn-secondary" onclick={handleClose}>Cancel</button>
            <button type="button" class="btn-primary" onclick={handleSave} disabled={!titleDraft.trim()}>Save</button>
        {/snippet}

        <BaseModal title="Edit Task" width="440px" footer={footer} onClose={handleClose} closeOnOverlayClick={false}>
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
            {#if !isAgentDraft}
            <div class="options">
                <div class="config-row">
                    <div class="config-info">
                        <span class="config-label">Operating mode</span>
                        <span class="config-desc">{modeDescription}</span>
                    </div>
                    <ModeSelect value={operatingModeDraft} onChange={(m: string) => operatingModeDraft = m} />
                </div>
            </div>
            {/if}
            <div class="options-section">
                <div class="section-label">Repository</div>
                {#if !taskHasParent}
                <div class="config-row">
                    <div class="config-info">
                        <span class="config-label">Branch</span>
                        <span class="config-desc">Base branch for this task's changes</span>
                    </div>
                    <div class="branch-input-wrapper">
                        <input
                            bind:this={branchInput}
                            type="text"
                            bind:value={branchDraft}
                            placeholder={targetBranch}
                            class="branch-input"
                            oninput={handleBranchInput}
                            onkeydown={handleBranchKeydown}
                            onfocus={openBranchDropdown}
                            onblur={() => setTimeout(() => closeBranchDropdown(), 150)}
                        />
                        {#if branchDropdownOpen && filteredBranches.length > 0}
                            <ul bind:this={branchDropdownRef} class="branch-dropdown" role="listbox">
                                {#each filteredBranches as branch, i (branch)}
                                    <li
                                        class="branch-option {i === branchActiveIndex ? 'active' : ''}"
                                        role="option"
                                        aria-selected={i === branchActiveIndex}
                                        onmousedown={(e) => { e.preventDefault(); selectBranch(branch); }}
                                    >{branch}</li>
                                {/each}
                            </ul>
                        {/if}
                    </div>
                </div>
                {/if}
                <div class="config-row">
                    <div class="config-info">
                        <span class="config-label">Auto-merge</span>
                        <span class="config-desc">{taskHasParent ? 'Merge changes into parent task when complete' : `Merge changes into ${branchDraft || targetBranch} when complete`}</span>
                    </div>
                    <ToggleSwitch bind:checked={autoMergeDraft} onChange={() => { userTouchedAutoMerge = true; }} ariaLabel="Auto-merge on completion" />
                </div>
                {#if !taskHasParent}
                <div class="config-row">
                    <div class="config-info">
                        <span class="config-label">Allow subtasks to auto-merge</span>
                        <span class="config-desc">Subtasks created under this task will auto-merge into it</span>
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
    .branch-input-wrapper {
        position: relative;
    }
    .branch-input {
        width: 160px;
        font-size: 0.8125rem;
        color: var(--color-text-primary);
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        padding: 5px 8px;
        font-family: var(--font-mono, monospace);
        outline: none;
        text-align: right;
    }
    .branch-input:focus {
        border-color: var(--color-accent);
    }
    .branch-dropdown {
        position: absolute;
        z-index: 100;
        max-height: 200px;
        overflow-y: auto;
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        box-shadow: 0 4px 12px rgba(0, 0, 0, 0.25);
        margin: 0;
        padding: 0;
        list-style: none;
        min-width: 160px;
    }
    .branch-option {
        padding: 6px 10px;
        font-size: 0.8125rem;
        font-family: var(--font-mono, monospace);
        color: var(--color-text-primary);
        cursor: pointer;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }
    .branch-option:hover,
    .branch-option.active {
        background: var(--color-surface-3);
        color: var(--color-accent);
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
