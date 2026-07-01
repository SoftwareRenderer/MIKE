<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import type { Config, Provider, Task } from '$lib/types';
    import { createTask, getTask, enqueueAction, getOfflineStatus, setOfflineStatus, generateId } from '$lib/api';
    import { config as configStore, showCreateTask, board as boardStore, selectedProject } from '$lib/store';
    import ModelSelector from './Chat/ModelSelector.svelte';
    import FileMentionDropdown from './FileMentionDropdown.svelte';
    import ToggleSwitch from './ToggleSwitch.svelte';
    import { createFocusTrap } from '$lib/a11y';
    import { detectMentionAtCursor, insertMention, getInputRect } from '$lib/utils/file-mention';

    let { projectId, onClose, onTaskCreated, parentId, onOptimisticTaskCreated, onReplaceOptimisticTask }: { projectId: string | undefined; onClose: () => void; onTaskCreated?: () => void; parentId?: string; onOptimisticTaskCreated?: (task: Task) => void; onReplaceOptimisticTask?: (optimisticId: string, realTask: Task) => void } = $props();

    let prompt = $state('');
    let autoCommit = $state(false);

    let planMode = $state(false);
    let autoMerge = $state(true);
    let subtasksAutoMerge = $state(true);
    let loading = $state(false);
    let error = $state('');
    let parentTitle = $state('');
    let attachments = $state<Array<{ mimeType: string; data: string }>>([]);
    let fileInput: HTMLInputElement | null = $state(null);

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
    let isAgentSelected = $derived(() => {
        if (!selectedUrl) return false;
        const p = providers.find(p => p.url === selectedUrl);
        return p ? p.kind !== 'llm' : false;
    });

    // Subscribe to config store to pick up providers (may load after modal opens)
    // $configStore syntax auto-subscribes to store changes
    let cfg: Config | null = $state(null);
    const unsubConfig = configStore.subscribe(val => { cfg = val; });
    let targetBranch = $derived($selectedProject?.origin_branch || 'main');
    let configLoaded = $state(false);
    $effect(() => {
        if (cfg?.providers) {
            providers = cfg.providers;
            // Pre-select highest-priority provider's default model (only if not already selected)
            if (providers.length > 0 && !selectedModel) {
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
        // Set defaults from config on first load only
        if (cfg && !configLoaded) {
            configLoaded = true;
            autoCommit = cfg.default_auto_commit ?? false;
            autoMerge = cfg.default_auto_merge ?? true;
            planMode = cfg.default_plan_mode ?? false;
        }
    });

    onMount(async () => {
        triggerEl = document.activeElement as HTMLElement;
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

    onDestroy(() => {
        unsubConfig();
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
        const taskData: Record<string, unknown> = {
            prompt: prompt.trim(),
            auto_commit: autoMerge, // auto-commit is prerequisite for auto-merge
            plan_mode: planMode,
            auto_merge: autoMerge,
            subtasks_auto_merge: subtasksAutoMerge,
        };
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
        const taskDataWithId = { ...taskData, id };
        const optimisticTask = { ...taskDataWithId, title: (taskData.prompt as string)?.slice(0, 80) || '', status: 'backlog', created_at: new Date().toISOString(), queued: true } as any;
        if (isOfflineNow) {
            const queued = enqueueAction({ type: 'createTask', params: { projectId, taskData: taskDataWithId } });
            (optimisticTask as any).actionId = queued.id;
        }
        boardStore.update(b => {
            if (!b) return b;
            return { ...b, tasks: [...(b.tasks || []), optimisticTask] };
        });
        onOptimisticTaskCreated?.(optimisticTask);
        showCreateTask.set(false);
        onTaskCreated?.();
        onClose();
        loading = false;

        // Fire-and-forget: try to create the task on the server in the background.
        // If online, replace the optimistic task with the real one.
        // If offline, createTask() returns the queued result (no-op since already queued above).
        if (!isOfflineNow) {
            createTask(projectId, taskData).then((realTask) => {
                // Replace optimistic task with real one in boardStore
                boardStore.update(b => {
                    if (!b) return b;
                    const idx = b.tasks.findIndex(t => t.id === id);
                    if (idx !== -1) {
                        const updated = [...b.tasks];
                        updated[idx] = realTask;
                        return { ...b, tasks: updated };
                    }
                    return b;
                });
                // Also replace in App's tasks array
                onReplaceOptimisticTask?.(id, realTask);
            }).catch(() => {
                // Server unreachable — queue the action as fallback
                setOfflineStatus(true);
                const queued = enqueueAction({ type: 'createTask', params: { projectId, taskData: taskDataWithId } });
                (optimisticTask as any).actionId = queued.id;
            });
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
                <textarea bind:this={promptTextarea} id="task-description" bind:value={prompt} placeholder="Describe the task the AI should execute..." rows="5" required aria-invalid={!!error} aria-describedby={error ? 'task-error' : undefined} oninput={handleMentionInput} onkeydown={handlePromptKeydown}></textarea>
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
                {#if !isAgentSelected()}
                <div class="config-row">
                    <div class="config-info">
                        <div class="config-text">
                            <span class="config-label">Plan mode</span>
                            <span class="config-desc">AI decomposes work into subtasks on the board, then executes after approval</span>
                        </div>
                    </div>
                    <ToggleSwitch bind:checked={planMode} ariaLabel="Plan mode" />
                </div>
                {/if}
            </div>
            <div class="options-section">
                <div class="section-label">Repository</div>
                <div class="config-row">
                    <div class="config-info">
                        <div class="config-text">
                            <span class="config-label">Auto-merge</span>
                            <span class="config-desc">
                                {#if parentId}
                                Merge changes into parent task when complete
                                {:else}
                                Merge changes into <span class="branch-badge" title={`Branch: ${targetBranch}`}><svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="6" cy="6" r="2.5"/><circle cx="10" cy="10" r="2.5"/><line x1="7.8" y1="7.8" x2="8.2" y2="8.2"/><line x1="6" y1="3.5" x2="6" y2="2"/><line x1="10" y1="12.5" x2="10" y2="14"/></svg>{targetBranch}</span> when complete
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
            <div class="modal-footer">
                <button type="button" onclick={handleClose} title="Cancel">Cancel</button>
                <button type="submit" class="btn-primary" disabled={loading || !prompt.trim() || (hasProviders && !selectedModel)} title="Create the task">
                    {loading ? 'Creating...' : 'Create Task'}
                </button>
            </div>
        </form>
    </div>
    <!-- @ file mention dropdown -->
    <FileMentionDropdown
        results={mentionResults}
        position={mentionPosition}
        activeIndex={mentionActiveIndex}
        onSelect={handleMentionSelect}
        onClose={handleMentionClose}
    />
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