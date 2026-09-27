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
    import { createTask, getTask, enqueueAction, getOfflineStatus, generateId, startTask, isTransientError, getLocalBranches } from '$lib/api';
    import { appStore, uiStore, dialogStore, removeTaskFromBoard, updateTaskInBoard, appendTaskToBoard } from '$lib/stores';
    import { t } from '$lib/i18n';
    import { ATTACHABLE_IMAGE_ACCEPT, imageSrc } from '$lib/utils/attachments';
    import { useImageAttachments } from '$lib/composables/use-image-attachments.svelte';
    import BaseModal from './BaseModal.svelte';
    import ModelSelector from './Chat/ModelSelector.svelte';
    import FileMentionDropdown from './FileMentionDropdown.svelte';
    import ToggleSwitch from './ToggleSwitch.svelte';
    import ModeSelect from './ModeSelect.svelte';
    import { useFileMention } from '$lib/composables/use-file-mention.svelte';
    import { resolveWorker, getModelName } from '$lib/utils/providers';
    import { subtaskStatusFor, subtaskExecutionStateFor } from '$lib/utils/subtask-status';
    import { applyExploreModeRule } from '$lib/utils/explore-mode';
    import { matchesCreateTaskSubmitKey, effectiveCreateTaskSubmitKey, formatShortcut, shortcutHintsHidden } from '$lib/utils/keyboard';
    import { uiInjector, INJECTION_POINTS } from '$lib/services/ui-injector.svelte';
    import { MODE_DESCRIPTIONS } from '$lib/constants/modes';
    import BranchPicker from './BranchPicker.svelte';

    let { projectId, onClose, onTaskCreated, parentId, currentBranch, initialPrompt = '', lineTask = false, onOpenTask = null }: { projectId: string | undefined; onClose: () => void; onTaskCreated?: () => void; parentId?: string; currentBranch?: string; initialPrompt?: string; lineTask?: boolean; onOpenTask?: ((task: Task) => void) | null } = $props();

    // One-time initialization from the prop (e.g. gutter line-selection prefill)
    const initialPromptValue = () => initialPrompt;
    let prompt = $state(initialPromptValue());
    // Captures the INITIAL prefill via closure (see above) — the $effect
    // compares the live prop against it to detect a NEW line selection.
    let lastInitialPrompt = $state(initialPromptValue());
    $effect(() => {
        if (initialPrompt && initialPrompt !== lastInitialPrompt) {
            lastInitialPrompt = initialPrompt;
            prompt = initialPrompt;
            promptTextarea?.setSelectionRange(0, 0);
        }
    });
    let autoCommit = $state(false);
    // Line-task variant: start the task immediately after creating it.
    let autoStart = $state(true);

    let operatingMode = $state('act'); // "act", "plan", or "explore"
    let modeChangedByUser = $state(false);
    let autoExploreBase = $state<string | null>(null);
    let autoMerge = $state(false);
    let subtasksAutoMerge = $state(true);
    let loading = $state(false);
    let parentTitle = $state('');
    // Fetched with the parent's title; decides the column of a new subtask until the
    // server's answer replaces it (see utils/subtask-status).
    let parentStatus = $state<string | undefined>(undefined);
    let attachments = $state<Array<{ mimeType: string; data: string }>>([]);
    let fileInput: HTMLInputElement | null = $state(null);

    // Branch selection
    let branchDraft = $state('');
    let branches = $state<string[]>([]);

    let promptTextarea: HTMLTextAreaElement | null = $state(null);

    // @ file mention — shared state machine (see use-file-mention.svelte.ts)
    const mention = useFileMention({
        getProjectId: () => projectId,
        getValue: () => prompt,
        setValue: (v) => { prompt = v; },
        getTextarea: () => promptTextarea,
    });

    function handlePromptInput() {
        if (!isAgentSelected) {
            const next = applyExploreModeRule(prompt, { mode: operatingMode, modeChangedByUser, autoExploreBase });
            operatingMode = next.mode;
            autoExploreBase = next.autoExploreBase;
        }
    }

    function handlePromptKeydown(e: KeyboardEvent) {
        mention.handleKeydown(e);
    }

    const intake = useImageAttachments({
        getTextarea: () => promptTextarea,
        getValue: () => prompt,
        setValue: (v) => { prompt = v; },
        held: () => attachments.length,
        addImages: (accepted) => { attachments = [...attachments, ...accepted]; },
        hasTask: () => false,
    });

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

    // Shared guard for the Create button and the submit shortcut (Settings → Preferences).
    const canSubmit = $derived(!loading && prompt.trim() !== '' && (!hasProviders || !!selectedModel));
    // Shortcut label for the Create button (tooltip + keycap); empty when disabled.
    const submitHintLabel = $derived.by(() => {
        const key = effectiveCreateTaskSubmitKey(appStore.config);
        return key ? formatShortcut(key) : '';
    });
    const hintsHidden = $derived(shortcutHintsHidden(appStore.config));

    // The submit shortcut fires from anywhere in the dialog (description included).
    $effect(() => {
        const handleKeydown = (e: KeyboardEvent) => {
            if (e.repeat || !canSubmit || dialogStore.confirmationSlot.pending) return;
            if (matchesCreateTaskSubmitKey(appStore.config, e)) {
                e.preventDefault();
                handleSubmit();
            }
        };
        window.addEventListener('keydown', handleKeydown);
        return () => window.removeEventListener('keydown', handleKeydown);
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
                } else if ((providers[0].models?.length ?? 0) > 0) {
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
        // Queue after BaseModal's focus-trap microtask (queued from its onMount, which
        // runs before ours) so the textarea wins initial focus over the close button.
        queueMicrotask(() => {
            promptTextarea?.focus();
            if (prompt) promptTextarea?.setSelectionRange(0, 0);
        });
        // Fetch parent task title and defaults if creating a subtask
        if (parentId && projectId) {
            try {
                const parentTask = await getTask(projectId, parentId);
                parentTitle = parentTask.title;
                parentStatus = parentTask.status;
                // Inherit auto_commit and auto_merge from parent
                autoCommit = parentTask.auto_commit ?? autoCommit;
                autoMerge = parentTask.subtasks_auto_merge ?? autoMerge;
            } catch {
                parentTitle = t('app.task.parentFallback');
            }
        }
        // Fetch available branches for root tasks
        if (!parentId && projectId) {
            branchDraft = targetBranch;
            try {
                branches = await getLocalBranches(projectId);
            } catch {
                // Ignore errors
            }
        }
    });

    function handleModelSelect(_providerName: string, modelName: string, providerUrl: string, reasoningEffort?: string) {
        selectedModel = modelName;
        selectedUrl = providerUrl;
        selectedReasoningEffort = reasoningEffort || '';
        // Agent providers must use explore mode
        if (isAgentSelected) {
            operatingMode = 'explore';
        }
    }

    /** Replace the optimistic board card with the real server task. */
    function replaceOptimisticTask(id: string, realTask: Task) {
        updateTaskInBoard(appStore, id, realTask);
    }

    /** Auto-start (line-task variant) — fire-and-forget with a failure toast. */
    function startTaskIfEnabled(taskProjectId: string, taskId: string) {
        if (!lineTask || !autoStart) return;
        startTask(taskProjectId, taskId).catch(() => {
            dialogStore.showToast(t('app.task.autoStartFailed'), 'error');
        });
    }

    async function handleSubmit() {
        const lineTaskFlow = lineTask;
        if (!projectId || !prompt.trim()) return;
        loading = true;
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
        // Send the column we will draw the card in, so the offline queue and its replay
        // agree with it. The server re-derives it from the parent either way.
        taskData.status = subtaskStatusFor(parentStatus);

        const isOfflineNow = getOfflineStatus();
        // Always show optimistic card immediately — close modal right away
        const id = generateId();
        // Include optimistic ID in taskData so getQueuedTaskIds() can find it
        const taskDataWithId = { ...taskData, id, title: (taskData.prompt as string)?.slice(0, 80) || '' };
        // Same column as the reply will use, plus the badge that goes with it.
        const optimisticTask = { ...taskDataWithId, execution_state: subtaskExecutionStateFor(parentStatus), created_at: new Date().toISOString(), queued: true } as any;

        const queueStartIfEnabled = async () => {
            if (lineTaskFlow && autoStart) {
                await enqueueAction({ type: 'startTask', params: { projectId, taskId: id } });
            }
        };
        if (isOfflineNow) {
            const queued = await enqueueAction({ type: 'createTask', params: { projectId, taskData: taskDataWithId } });
            (optimisticTask as any).actionId = queued.id;
            await queueStartIfEnabled();
        }
        appendTaskToBoard(appStore, optimisticTask);
        uiStore.showCreateTask = false;
        onTaskCreated?.();
        onClose();
        loading = false;

        if (isOfflineNow) {
            if (lineTaskFlow) {
                dialogStore.showToast(t('app.task.queuedOffline'));
            }
            return;
        }

        /** A create that did not land. A network failure is queued for retry; anything
         *  else is the server refusing it, and a refusal would sit in the persisted
         *  queue forever promising a task that never exists - so the card goes with it. */
        const handleCreateFailure = async (err: unknown, announceOffline: boolean) => {
            if (!isTransientError(err)) {
                const reason = err instanceof Error ? err.message : String(err);
                removeTaskFromBoard(appStore, id);
                dialogStore.showToast(t('app.task.createFailed', { error: reason }), 'error');
                return;
            }
            // Nothing marks the app offline here: the failed request already reported its own reason.
            const queued = await enqueueAction({ type: 'createTask', params: { projectId, taskData: taskDataWithId } });
            (optimisticTask as any).actionId = queued.id;
            await queueStartIfEnabled();
            if (announceOffline) dialogStore.showToast(t('app.task.queuedOffline'));
        };

        if (lineTaskFlow) {
            // The line-task variant needs the real ID to open the floating task
            try {
                const realTask = await createTask(projectId, taskData);
                replaceOptimisticTask(id, realTask);
                startTaskIfEnabled(projectId, realTask.id);
                onOpenTask?.(realTask);
            } catch (err) {
                await handleCreateFailure(err, true);
            }
            return;
        }

        // Fire-and-forget: try to create the task on the server in the background.
        // If online, replace the optimistic task with the real one.
        // If offline, createTask() returns the queued result (no-op since already queued above).
        createTask(projectId, taskData).then((realTask) => {
            replaceOptimisticTask(id, realTask);
            startTaskIfEnabled(projectId, realTask.id);
        }).catch((err) => handleCreateFailure(err, false));
    }

    /**
     * Gate run before the modal closes (Esc, X, Cancel). Warns that changes
     * will be lost when the description isn't blank. Resolves true when the
     * modal may close.
     */
    async function requestClose(): Promise<boolean> {
        if (prompt.trim()) {
            if (dialogStore.confirmationSlot.pending) return false;
            const confirmed = await dialogStore.showConfirm({
                title: t('app.task.discardTitle'),
                message: t('app.task.discardMessage'),
                confirmText: t('app.discard'),
                cancelText: t('app.keepEditing'),
            });
            if (!confirmed) return false;
        }
        return true;
    }

    function closeModal() {
        onClose();
    }

    async function handleCancel() {
        if (await requestClose()) closeModal();
    }
</script>

{#snippet footer()}
    <button type="button" class="btn" onclick={handleCancel} title="Cancel">{t('app.cancel')}</button>
    {#if lineTask}
        <span class="auto-start-option" role="button" tabindex="-1" title="Start the task immediately after creating it" onclick={() => { autoStart = !autoStart; }} onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); autoStart = !autoStart; } }}>
            <span class="auto-start-label">{t('app.task.autoStart')}</span>
            <ToggleSwitch bind:checked={autoStart} ariaLabel="Auto start after creating" />
        </span>
    {/if}
    <button type="button" class="btn btn-primary" disabled={!canSubmit} onclick={() => handleSubmit()} title={`Create the task${submitHintLabel ? ` (${submitHintLabel})` : ''}`}>
        {loading ? t('app.task.creating') : t('app.task.create')}{#if submitHintLabel && !hintsHidden}<kbd class="key-hint">{submitHintLabel}</kbd>{/if}
    </button>
{/snippet}

<BaseModal title={parentId ? t('app.task.createSubtask') : t('app.task.create')} width="480px" footer={footer} onClose={closeModal} onBeforeClose={requestClose} closeOnOverlayClick={false} floating={lineTask}>
    {#if parentId}
        <div class="parent-label">{t('app.task.subtaskOf')} <strong>{parentTitle || t('app.task.parentFallback')}</strong></div>
    {/if}
    <label for="task-description">
        <span class="label-row">
            {t('app.task.description')}
            <button type="button" class="btn-attach" onclick={() => fileInput?.click()} aria-label={t('chat.common.attachImage')} title={t('chat.common.attachImage')}>
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                    <path d="M21.44 11.05l-9.19 9.19a6 6 0 01-8.49-8.49l9.19-9.19a4 4 0 015.66 5.66l-9.2 9.19a2 2 0 01-2.83-2.83l8.49-8.48"/>
                </svg>
            </button>
        </span>
        {#if attachments.length > 0}
            <div class="task-attachments">
                {#each attachments as att, i}
                    <div class="task-attachment-chip">
                        <img src={imageSrc(att)} alt="Attachment" />
                        <button class="remove-attachment" onclick={() => removeAttachment(i)} title="Remove">&times;</button>
                    </div>
                {/each}
            </div>
        {/if}
        <textarea bind:this={promptTextarea} id="task-description" class="image-drop-target" class:is-drop-target={intake.isOver} bind:value={prompt} placeholder="Describe the task the AI should execute..." rows="5" required oninput={() => { handlePromptInput(); mention.handleInput(); }} onkeydown={handlePromptKeydown} onpaste={intake.handlePaste} ondragenter={intake.handleDragEnter} ondragover={intake.handleDragOver} ondragleave={intake.handleDragLeave} ondrop={intake.handleDrop}></textarea>
        <!-- Anchor for sandboxed UI extensions (e.g. GitHub issue importer) -->
        <div bind:this={extAnchor} class="ext-anchor"></div>
    </label>
    <input type="file" accept={ATTACHABLE_IMAGE_ACCEPT} multiple bind:this={fileInput} onchange={intake.handlePicked} style="display:none" />
    {#if hasProviders}
        <div class="model-select-row">
            <span id="model-label" class="model-label" style="margin:0">{t('app.task.model')}</span>
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
                    <span class="config-label">{t('app.task.operatingMode')}</span>
                    <span class="config-desc">{MODE_DESCRIPTIONS[operatingMode] || ''}</span>
                </div>
            </div>
            <ModeSelect value={operatingMode} onChange={(m: string) => { modeChangedByUser = true; operatingMode = m; }} />
        </div>
        {/if}
    </div>
    <div class="options-section">
        <div class="section-label">{t('app.task.repository')}</div>
        {#if !parentId}
        <div class="config-row">
            <div class="config-info">
                <div class="config-text">
                    <span class="config-label">{t('app.task.branch')}</span>
                    <span class="config-desc">{t('app.task.branchDesc')}</span>
                </div>
            </div>
            <BranchPicker bind:value={branchDraft} {branches} placeholder={targetBranch} />
        </div>
        {/if}
        <div class="config-row">
            <div class="config-info">
                <div class="config-text">
                    <span class="config-label">{t('app.task.autoMerge')}</span>
                    <span class="config-desc">
                        {#if parentId}
                        {t('app.task.autoMergeParentDesc')}
                        {:else}
                        {t('app.task.autoMergeInto.before')} <span class="branch-badge" title={`Branch: ${branchDraft || targetBranch}`}><svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="6" cy="6" r="2.5"/><circle cx="10" cy="10" r="2.5"/><line x1="7.8" y1="7.8" x2="8.2" y2="8.2"/><line x1="6" y1="3.5" x2="6" y2="2"/><line x1="10" y1="12.5" x2="10" y2="14"/></svg>{branchDraft || targetBranch}</span> {t('app.task.autoMergeInto.after')}
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
                    <span class="config-label">{t('app.task.subtasksAutoMerge')}</span>
                    <span class="config-desc">{t('app.task.subtasksAutoMergeDesc')}</span>
                </div>
            </div>
            <ToggleSwitch bind:checked={subtasksAutoMerge} ariaLabel="Allow subtasks to auto-merge" />
        </div>
        {/if}
    </div>
</BaseModal>

<!-- @ file mention dropdown — must be outside BaseModal (floating element) -->
<FileMentionDropdown {...mention.dropdownProps} />

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
    .btn-primary .key-hint {
        color: rgba(255, 255, 255, 0.85);
        border-color: rgba(255, 255, 255, 0.3);
        background: rgba(255, 255, 255, 0.1);
    }
    .auto-start-option {
        display: inline-flex;
        align-items: center;
        gap: 8px;
        margin-left: auto;
        font-size: var(--text-sm);
        font-weight: 500;
        color: var(--color-text-secondary);
        cursor: pointer;
        user-select: none;
    }
    label {
        display: flex;
        flex-direction: column;
        gap: 6px;
        font-size: var(--text-base);
        font-weight: 500;
        color: var(--color-text-secondary);
    }
    textarea {
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        padding: 8px 12px;
        color: var(--color-text-primary);
        font-size: var(--text-base);
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
        font-size: var(--text-base);
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
        font-size: var(--text-sm);
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
        font-size: var(--text-xs);
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
        font-size: var(--text-sm);
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
        font-size: var(--text-xs);
        font-weight: 500;
        color: var(--color-text-secondary);
        font-family: var(--font-mono);
        vertical-align: baseline;
        line-height: 1.2;
    }
    .config-desc {
        font-size: var(--text-xs);
        color: var(--color-text-tertiary);
    }
    .model-select-row {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 12px;
        font-size: var(--text-base);
        font-weight: 500;
        color: var(--color-text-secondary);
    }
    @media (max-width: 768px) {
        :global(.modal-body) {
            padding: 16px;
        }
    }
</style>
