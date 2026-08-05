<script lang="ts">
    import { onMount } from 'svelte';
    // Anchor element where sandboxed UI extensions mount their iframes.
    let extAnchor: HTMLElement | null = $state(null);
    // UI extension injection point — re-render when extensions change.
    // The returned cleanup tears down the extension channels on unmount.
    $effect(() => {
        const _v = uiInjector.getPointVersion(INJECTION_POINTS.CREATE_TASK);
        uiInjector.refreshSlot(INJECTION_POINTS.CREATE_TASK, { component: 'CreateTaskModal' }, extAnchor);
        return () => uiInjector.teardownSlot(INJECTION_POINTS.CREATE_TASK);
    });
    import type { Config, Provider, Task } from '$lib/types';
    import { AGENT_URL_PREFIX, isAgentProvider } from '$lib/types';
    import { createTask, getTask, enqueueAction, getOfflineStatus, setOfflineStatus, generateId } from '$lib/api';
    import { appStore, uiStore } from '$lib/stores';
    import BaseModal from './BaseModal.svelte';
    import ModelSelector from './Chat/ModelSelector.svelte';
    import FileMentionDropdown from './FileMentionDropdown.svelte';
    import ToggleSwitch from './ToggleSwitch.svelte';
    import ModeSelect from './ModeSelect.svelte';
    import { detectMentionAtCursor, insertMention, getInputRect } from '$lib/utils/file-mention';
    import { resolveWorker, getModelName } from '$lib/utils/providers';
    import { uiInjector, INJECTION_POINTS } from '$lib/services/ui-injector.svelte';
    import { MODE_DESCRIPTIONS } from '$lib/constants/modes';

    let { projectId, onClose, onTaskCreated, parentId, currentBranch, onOptimisticTaskCreated, onReplaceOptimisticTask }: { projectId: string | undefined; onClose: () => void; onTaskCreated?: () => void; parentId?: string; currentBranch?: string; onOptimisticTaskCreated?: (task: Task) => void; onReplaceOptimisticTask?: (optimisticId: string, realTask: Task) => void } = $props();

    let prompt = $state('');
    let autoCommit = $state(false);

    let operatingMode = $state('act'); // "act", "plan", or "explore"
    let autoMerge = $state(false);
    let subtasksAutoMerge = $state(true);
    let loading = $state(false);
    let error = $state('');
    let parentTitle = $state('');
    let attachments = $state<Array<{ mimeType: string; data: string }>>([]);
    let fileInput: HTMLInputElement | null = $state(null);

    // Branch selection
    let branchDraft = $state('');
    let branches = $state<string[]>([]);
    // svelte-ignore non_reactive_update
    let branchInput: HTMLInputElement | null = null;
    let branchDropdownOpen = $state(false);
    let branchActiveIndex = $state(0);
    // svelte-ignore non_reactive_update
    let branchDropdownRef: HTMLUListElement | null = null;
    let branchWrapperRef: HTMLDivElement | null = null;

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

    // @ file mention state
    let mentionTrigger = $state<{ atIndex: number; query: string } | null>(null);
    let mentionResults = $state<Array<{ path: string }>>([]);
    let mentionPosition = $state<{ top: number; bottom: number; left: number; width: number } | null>(null);
    let mentionActiveIndex = $state(0);
    let mentionSearchTimer: ReturnType<typeof setTimeout> | null = null;
    let promptTextarea: HTMLTextAreaElement | null = $state(null);
    const MENTION_DEBOUNCE_MS = 150;

    function handleMentionInput() {
        if (!promptTextarea) return;
        const cursorPos = promptTextarea.selectionStart ?? 0;
        const trigger = detectMentionAtCursor(prompt, cursorPos);

        if (trigger) {
            mentionTrigger = trigger;
            mentionActiveIndex = 0;
            const rect = getInputRect(promptTextarea);
            mentionPosition = rect;

            if (mentionSearchTimer) clearTimeout(mentionSearchTimer);
            mentionSearchTimer = setTimeout(() => {
                searchFiles(trigger.query);
            }, MENTION_DEBOUNCE_MS);
        } else {
            closeMention();
        }
    }

    async function searchFiles(query: string) {
        if (!projectId) return;
        const params = new URLSearchParams({ q: query, project: projectId });
        try {
            const res = await fetch(`/api/fs/search?${params.toString()}`);
            if (!res.ok) return;
            const data = await res.json();
            mentionResults = data.results || [];
        } catch {
            mentionResults = [];
        }
    }

    function closeMention() {
        mentionTrigger = null;
        mentionResults = [];
        mentionPosition = null;
        mentionActiveIndex = 0;
        if (mentionSearchTimer) {
            clearTimeout(mentionSearchTimer);
            mentionSearchTimer = null;
        }
    }

    function handleMentionSelect(path: string) {
        if (!mentionTrigger || !promptTextarea) return;
        const { value: newValue, cursorPos } = insertMention(prompt, mentionTrigger.atIndex, mentionTrigger.query, path);
        prompt = newValue;
        closeMention();
        requestAnimationFrame(() => {
            if (promptTextarea) {
                promptTextarea.focus();
                promptTextarea.setSelectionRange(cursorPos, cursorPos);
            }
        });
    }

    function handleMentionClose() {
        closeMention();
        promptTextarea?.focus();
    }

    function handlePromptKeydown(e: KeyboardEvent) {
        if (mentionTrigger && mentionResults.length > 0) {
            if (e.key === 'ArrowDown') {
                e.preventDefault();
                mentionActiveIndex = (mentionActiveIndex + 1) % mentionResults.length;
                return;
            }
            if (e.key === 'ArrowUp') {
                e.preventDefault();
                mentionActiveIndex = (mentionActiveIndex - 1 + mentionResults.length) % mentionResults.length;
                return;
            }
            if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault();
                if (mentionActiveIndex >= 0 && mentionActiveIndex < mentionResults.length) {
                    handleMentionSelect(mentionResults[mentionActiveIndex].path);
                }
                return;
            }
            if (e.key === 'Escape') {
                e.preventDefault();
                e.stopPropagation();
                closeMention();
                return;
            }
        }
    }

    async function handlePaste(e: ClipboardEvent) {
        // Handled by UI extension
    }

    function handleFileSelect(e: Event) {
        const files = (e.target as HTMLInputElement).files;
        if (!files || files.length === 0) return;
        const remaining = 4 - attachments.length;
        for (let i = 0; i < Math.min(remaining, files.length); i++) {
            const file = files[i];
            if (file.size > 4 * 1024 * 1024) continue;
            const reader = new FileReader();
            reader.onload = () => {
                attachments = [...attachments, { mimeType: file.type, data: reader.result as string }];
            };
            reader.readAsDataURL(file);
        }
        if (fileInput) fileInput.value = '';
    }

    function removeAttachment(index: number) {
        attachments = attachments.filter((_, i) => i !== index);
    }

    // Model selection state
    let providers: Provider[] = $state([]);
    let selectedModel = $state('');
    let selectedUrl = $state('');
    let selectedReasoningEffort = $state('');
    let hasProviders = $derived(providers.length > 0);
    let isAgentSelected = $derived.by(() => {
        if (!selectedUrl) return false;
        if (selectedUrl.startsWith(AGENT_URL_PREFIX)) return true;
        const provider = providers.find(p => p.url === selectedUrl);
        return provider ? isAgentProvider(provider) : false;
    });

    // Config is now reactive via appStore
    let cfg: Config | null = $state(null);
    $effect(() => { cfg = appStore.config; });
    let targetBranch = $derived(currentBranch || appStore.selectedProject?.origin_branch || 'main');
    let configLoaded = $state(false);
    $effect(() => {
        if (cfg?.providers) {
            providers = cfg.providers;
            // Pre-select the configured Worker provider/model (only if not already selected)
            if (providers.length > 0 && !selectedModel) {
                const worker = resolveWorker(cfg);
                if (worker.provider && worker.modelName) {
                    selectedUrl = worker.url;
                    selectedModel = worker.modelName;
                } else if (providers[0].models.length > 0) {
                    // Fallback: first available model of the first provider
                    selectedUrl = providers[0].url;
                    selectedModel = getModelName(providers[0].models[0]);
                }
            }
        }
        // Set defaults from config on first load only
        if (cfg && !configLoaded) {
            configLoaded = true;
            autoCommit = cfg.default_auto_commit ?? false;
            autoMerge = cfg.default_auto_merge ?? false;
            operatingMode = cfg.default_operating_mode ?? 'act';
        }
    });

    onMount(async () => {
        // Fetch parent task title and defaults if creating a subtask
        if (parentId && projectId) {
            try {
                const parentTask = await getTask(projectId, parentId);
                parentTitle = parentTask.title;
                // Inherit auto_commit and auto_merge from parent
                autoCommit = parentTask.auto_commit ?? autoCommit;
                autoMerge = parentTask.subtasks_auto_merge ?? autoMerge;
            } catch {
                parentTitle = 'Parent task';
            }
        }
        // Fetch available branches for root tasks
        if (!parentId && projectId) {
            branchDraft = targetBranch;
            try {
                const res = await fetch(`/api/projects/${projectId}/git/branches`);
                if (res.ok) {
                    const data: string[] = await res.json();
                    branches = data.filter(b => !b.startsWith('origin/') && b !== 'HEAD');
                }
            } catch {
                // Ignore errors
            }
        }
    });



    function handleModelSelect(providerName: string, modelName: string, providerUrl: string, reasoningEffort?: string) {
        selectedModel = modelName;
        selectedUrl = providerUrl;
        selectedReasoningEffort = reasoningEffort || '';
        // Agent providers must use explore mode
        if (isAgentSelected) {
            operatingMode = 'explore';
        }
    }

    async function handleSubmit() {
        if (!projectId || !prompt.trim()) return;
        loading = true;
        error = '';
        const taskData: Record<string, unknown> = {
            prompt: prompt.trim(),
            auto_commit: autoMerge, // auto-commit is prerequisite for auto-merge
            operating_mode: operatingMode,
            auto_merge: autoMerge,
            subtasks_auto_merge: subtasksAutoMerge,
        };
        if (!parentId && branchDraft.trim()) {
            taskData.base_ref = branchDraft.trim();
        }
        if (attachments.length > 0) {
            taskData.attachments = JSON.stringify(attachments);
        }
        if (selectedModel) taskData.model_name = selectedModel;
        if (selectedUrl) taskData.llama_server_url = selectedUrl;
        if (selectedReasoningEffort) taskData.reasoning_effort = selectedReasoningEffort;
        if (parentId) taskData.parent_task_id = parentId;

        const isOfflineNow = getOfflineStatus();
        // Always show optimistic card immediately — close modal right away
        const id = generateId();
        // Include optimistic ID in taskData so getQueuedTaskIds() can find it
        const taskDataWithId = { ...taskData, id, title: (taskData.prompt as string)?.slice(0, 80) || '' };
        const optimisticTask = { ...taskDataWithId, title: (taskData.prompt as string)?.slice(0, 80) || '', status: 'backlog', created_at: new Date().toISOString(), queued: true } as any;
        if (isOfflineNow) {
            const queued = await enqueueAction({ type: 'createTask', params: { projectId, taskData: taskDataWithId } });
            (optimisticTask as any).actionId = queued.id;
        }
        appStore.board = { ...appStore.board!, tasks: [...(appStore.board!.tasks || []), optimisticTask] };
        onOptimisticTaskCreated?.(optimisticTask);
        uiStore.showCreateTask = false;
        onTaskCreated?.();
        onClose();
        loading = false;

        // Fire-and-forget: try to create the task on the server in the background.
        // If online, replace the optimistic task with the real one.
        // If offline, createTask() returns the queued result (no-op since already queued above).
        if (!isOfflineNow) {
            createTask(projectId, taskData).then((realTask) => {
                // Replace optimistic task with real one in appStore
                const idx = appStore.board?.tasks.findIndex(t => t.id === id) ?? -1;
                if (idx !== -1 && appStore.board) {
                    const updated = [...appStore.board.tasks];
                    updated[idx] = realTask;
                    appStore.board = { ...appStore.board, tasks: updated };
                }
                // Also replace in App's tasks array
                onReplaceOptimisticTask?.(id, realTask);
            }).catch(async () => {
                // Server unreachable — queue the action as fallback
                setOfflineStatus(true);
                const queued = await enqueueAction({ type: 'createTask', params: { projectId, taskData: taskDataWithId } });
                (optimisticTask as any).actionId = queued.id;
            });
        }
    }

    function handleClose() {
        onClose();
    }
</script>

{#snippet footer()}
    <button type="button" class="btn-secondary" onclick={handleClose} title="Cancel">Cancel</button>
    <button type="button" class="btn-primary" disabled={loading || !prompt.trim() || (hasProviders && !selectedModel)} onclick={handleSubmit} title="Create the task">
        {loading ? 'Creating...' : 'Create Task'}
    </button>
{/snippet}

<BaseModal title={parentId ? 'Create Subtask' : 'Create Task'} width="480px" footer={footer} onClose={handleClose} closeOnOverlayClick={false}>
    {#if parentId}
        <div class="parent-label">Subtask of: <strong>{parentTitle || 'Parent task'}</strong></div>
    {/if}
    <label for="task-description">
        <span class="label-row">
            Description
            <button type="button" class="btn-attach" onclick={() => fileInput?.click()} aria-label="Attach image" title="Attach image">
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                    <path d="M21.44 11.05l-9.19 9.19a6 6 0 01-8.49-8.49l9.19-9.19a4 4 0 015.66 5.66l-9.2 9.19a2 2 0 01-2.83-2.83l8.49-8.48"/>
                </svg>
            </button>
        </span>
        {#if attachments.length > 0}
            <div class="task-attachments">
                {#each attachments as att, i}
                    <div class="task-attachment-chip">
                        <img src={att.data} alt="Attachment" />
                        <button class="remove-attachment" onclick={() => removeAttachment(i)} title="Remove">&times;</button>
                    </div>
                {/each}
            </div>
        {/if}
        <textarea bind:this={promptTextarea} id="task-description" bind:value={prompt} placeholder="Describe the task the AI should execute..." rows="5" required aria-invalid={!!error} aria-describedby={error ? 'task-error' : undefined} oninput={handleMentionInput} onkeydown={handlePromptKeydown} onpaste={handlePaste}></textarea>
        <!-- Anchor for sandboxed UI extensions (e.g. GitHub issue importer) -->
        <div bind:this={extAnchor} class="ext-anchor"></div>
    </label>
    <input type="file" accept="image/*" multiple bind:this={fileInput} onchange={handleFileSelect} style="display:none" />
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
        {#if !isAgentSelected}
        <div class="config-row">
            <div class="config-info">
                <div class="config-text">
                    <span class="config-label">Operating mode</span>
                    <span class="config-desc">{MODE_DESCRIPTIONS[operatingMode] || ''}</span>
                </div>
            </div>
            <ModeSelect value={operatingMode} onChange={(m: string) => operatingMode = m} />
        </div>
        {/if}
    </div>
    <div class="options-section">
        <div class="section-label">Repository</div>
        {#if !parentId}
        <div class="config-row">
            <div class="config-info">
                <div class="config-text">
                    <span class="config-label">Branch</span>
                    <span class="config-desc">Base branch for this task's changes</span>
                </div>
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
                <div class="config-text">
                    <span class="config-label">Auto-merge</span>
                    <span class="config-desc">
                        {#if parentId}
                        Merge changes into parent task when complete
                        {:else}
                        Merge changes into <span class="branch-badge" title={`Branch: ${branchDraft || targetBranch}`}><svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="6" cy="6" r="2.5"/><circle cx="10" cy="10" r="2.5"/><line x1="7.8" y1="7.8" x2="8.2" y2="8.2"/><line x1="6" y1="3.5" x2="6" y2="2"/><line x1="10" y1="12.5" x2="10" y2="14"/></svg>{branchDraft || targetBranch}</span> when complete
                        {/if}
                    </span>
                </div>
            </div>
            <ToggleSwitch bind:checked={autoMerge} ariaLabel="Auto-merge on completion" />
        </div>
        {#if !parentId}
        <div class="config-row">
            <div class="config-info">
                <div class="config-text">
                    <span class="config-label">Allow subtasks to auto-merge</span>
                    <span class="config-desc">Subtasks created under this task will auto-merge into it</span>
                </div>
            </div>
            <ToggleSwitch bind:checked={subtasksAutoMerge} ariaLabel="Allow subtasks to auto-merge" />
        </div>
        {/if}
    </div>
    {#if error}
        <div id="task-error" class="error" role="alert">{error}</div>
    {/if}
</BaseModal>

<!-- @ file mention dropdown — must be outside BaseModal (floating element) -->
<FileMentionDropdown
    results={mentionResults}
    position={mentionPosition}
    activeIndex={mentionActiveIndex}
    onSelect={handleMentionSelect}
    onClose={handleMentionClose}
/>

<style>
    @import '$lib/styles/shared.css';

    :global(.modal) {
        box-shadow: 0 12px 40px rgba(0,0,0,0.3);
    }
    :global(.modal-body) {
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
        resize: vertical;
        min-height: 80px;
        font-family: inherit;
    }
    textarea:focus-visible {
        border-color: var(--color-accent);
        outline: none;
    }
    .ext-anchor {
        display: flex;
        flex-direction: column;
        gap: 4px;
    }
    .task-attachments {
        display: flex;
        flex-wrap: wrap;
        gap: 6px;
        margin-bottom: 8px;
    }
    .task-attachment-chip {
        position: relative;
        width: 48px;
        height: 48px;
        border-radius: var(--radius-sm);
        overflow: hidden;
        border: 1px solid var(--color-border);
    }
    .task-attachment-chip img {
        width: 100%;
        height: 100%;
        object-fit: cover;
    }
    .task-attachment-chip .remove-attachment {
        position: absolute;
        top: 0;
        right: 0;
        width: 18px;
        height: 18px;
        border-radius: 50%;
        background: rgba(0,0,0,0.7);
        color: white;
        border: none;
        font-size: 14px;
        line-height: 1;
        cursor: pointer;
        display: flex;
        align-items: center;
        justify-content: center;
        padding: 0;
    }
    .label-row {
        display: flex;
        justify-content: space-between;
        align-items: center;
    }
    .btn-attach {
        background: none;
        border: none;
        cursor: pointer;
        padding: 2px;
        color: var(--color-text-tertiary);
        display: flex;
        align-items: center;
    }
    .btn-attach:hover {
        color: var(--color-text-primary);
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
    .options-section {
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
    .config-info {
        display: flex;
        flex: 1;
        min-width: 0;
    }
    .config-text {
        display: flex;
        flex-direction: column;
        gap: 2px;
    }
    .config-label {
        font-size: 0.8125rem;
        font-weight: 500;
        color: var(--color-text-primary);
    }
    .branch-badge {
        display: inline-flex;
        align-items: center;
        gap: 3px;
        padding: 0 5px;
        border-radius: var(--radius-sm);
        background: var(--color-surface-3);
        font-size: 0.75rem;
        font-weight: 500;
        color: var(--color-text-secondary);
        font-family: var(--font-mono);
        vertical-align: baseline;
        line-height: 1.2;
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
    .config-desc {
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
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
    .error {
        color: var(--color-status-red);
        font-size: 0.875rem;
    }

    @media (max-width: 768px) {
        :global(.modal-body) {
            padding: 16px;
        }
    }
</style>
