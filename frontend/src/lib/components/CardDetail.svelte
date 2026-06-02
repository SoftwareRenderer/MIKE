
<script lang="ts">
    import { onMount } from 'svelte';
    import type { Task } from '$lib/types';
    import { selectedTask, showCardDetail, board as boardStore } from '$lib/store';
    import { moveTask, deleteTask, startTask, stopTask, retryTask, resumeTask, getTaskDiff } from '$lib/api';
    import TaskStream from './TaskStream.svelte';

    let task: Task | null = $state(null);
    let diffText = $state('');
    let loadingDiff = $state(false);
    let editingTitle = $state(false);
    let titleDraft = $state('');
    let badge = $state({ text: 'Idle', color: 'var(--color-text-tertiary)' });

    function close() {
        showCardDetail.set(false);
        selectedTask.set(null);
    }

    onMount(() => {
        // Subscribe to selectedTask store
        const unsub = selectedTask.subscribe(t => {
            if (t) {
                task = t;
                titleDraft = t.title;
                editingTitle = false;
                diffText = '';
                badge = getStatusBadge(t);
            }
        });

        // Listen for keyboard shortcuts
        function handleKey(e: KeyboardEvent) {
            if (e.key === 'Escape') close();
        }
        window.addEventListener('keydown', handleKey);
        return () => {
            unsub();
            window.removeEventListener('keydown', handleKey);
        };
    });

    async function handleMove(status: string) {
        if (!task) return;
        try {
            const updated = await moveTask(task.project_id, task.id, status);
            task = updated;
            selectedTask.set(updated);
            boardStore.update(b => {
                if (!b) return b;
                const idx = b.tasks.findIndex(t => t.id === task!.id);
                if (idx !== -1) b.tasks[idx] = updated;
                return { ...b };
            });
        } catch (e) {
            console.error('Failed to move:', e);
        }
    }

    async function handleStart() {
        if (!task) return;
        try {
            const updated = await startTask(task.project_id, task.id);
            task = updated;
            selectedTask.set(updated);
            boardStore.update(b => {
                if (!b) return b;
                const idx = b.tasks.findIndex(t => t.id === task!.id);
                if (idx !== -1) b.tasks[idx] = updated;
                return { ...b };
            });
        } catch (e) {
            console.error('Failed to start:', e);
        }
    }

    async function handleStop() {
        if (!task) return;
        try {
            const updated = await stopTask(task.project_id, task.id);
            task = updated;
            selectedTask.set(updated);
        } catch (e) {
            console.error('Failed to stop:', e);
        }
    }

    async function handleRetry() {
        if (!task) return;
        try {
            const updated = await retryTask(task.project_id, task.id);
            task = updated;
            selectedTask.set(updated);
        } catch (e) {
            console.error('Failed to retry:', e);
        }
    }

    async function handleResume() {
        if (!task) return;
        try {
            const updated = await resumeTask(task.project_id, task.id);
            task = updated;
            selectedTask.set(updated);
        } catch (e) {
            console.error('Failed to resume:', e);
        }
    }

    async function handleDelete() {
        if (!task || !confirm('Delete this task? This cannot be undone.')) return;
        try {
            await deleteTask(task.project_id, task.id);
            boardStore.update(b => {
                if (!b) return b;
                b.tasks = b.tasks.filter(t => t.id !== task!.id);
                return { ...b };
            });
            close();
        } catch (e) {
            console.error('Failed to delete:', e);
        }
    }

    async function loadDiff() {
        if (!task || loadingDiff) return;
        loadingDiff = true;
        try {
            diffText = await getTaskDiff(task.project_id, task.id);
        } catch (e) {
            diffText = 'Failed to load diff';
        }
        loadingDiff = false;
    }

    function saveTitle() {
        if (!task || !titleDraft.trim()) return;
        editingTitle = false;
        // TODO: call updateTask API
        task.title = titleDraft.trim();
    }

    function getStatusBadge(task: Task): { text: string; color: string } {
        switch (task.status_state) {
            case 'running': return { text: 'Running', color: 'var(--color-status-blue)' };
            case 'queued': return { text: 'Queued', color: 'var(--color-status-yellow)' };
            case 'paused': return { text: 'Paused', color: 'var(--color-status-purple)' };
            case 'awaiting_review': return { text: 'Review', color: 'var(--color-status-orange)' };
            case 'failed': return { text: 'Failed', color: 'var(--color-status-red)' };
            case 'completed': return { text: 'Completed', color: 'var(--color-status-green)' };
            default: return { text: 'Idle', color: 'var(--color-text-tertiary)' };
        }
    }
</script>

{#if task}
    <div class="overlay" onclick={close}></div>
    <div class="card-detail">
        <div class="header">
            {#if editingTitle}
                <input
                    class="title-input"
                    bind:value={titleDraft}
                    onblur={saveTitle}
                    autofocus
                />
            {:else}
                <h3 class="title" onclick={() => { editingTitle = true; titleDraft = task.title; }}>{task.title}</h3>
            {/if}
            <div class="header-right">
                <span class="badge" style="background:{badge.color}22;color:{badge.color}">{badge.text}</span>
                <button class="close-btn" onclick={close}>X</button>
            </div>
        </div>

        <div class="body">
            <!-- Prompt Section -->
            <div class="section">
                <h4>Prompt</h4>
                <div class="prompt-text">{task.prompt}</div>
            </div>

            <!-- Stream Section (running/paused/queued) -->
            {#if task.status_state === 'running' || task.status_state === 'paused' || task.status_state === 'queued'}
                <div class="section">
                    <h4>Live Output</h4>
                    <TaskStream projectId={task.project_id} taskId={task.id} />
                </div>
            {/if}

            <!-- Output Section (completed/review) -->
            {#if task.status_state === 'awaiting_review' || task.status_state === 'completed'}
                <div class="section">
                    <h4>Output</h4>
                    <div class="output-text">{task.output || 'No output'}</div>
                </div>
            {/if}

            <!-- Failed state message -->
            {#if task.status_state === 'failed'}
                <div class="section">
                    <h4>Error</h4>
                    <div class="error-text">{task.output || 'Task failed'}</div>
                </div>
            {/if}

            <!-- Diff Section (review) -->
            {#if task.status_state === 'awaiting_review'}
                <div class="section">
                    <h4>
                        Diff
                        <button class="btn-sm" onclick={loadDiff} disabled={loadingDiff}>
                            {loadingDiff ? 'Loading...' : 'Load Diff'}
                        </button>
                    </h4>
                    {#if diffText}
                        <pre class="diff">{diffText}</pre>
                    {/if}
                </div>
            {/if}

            <!-- Metadata -->
            <div class="section metadata">
                <h4>Metadata</h4>
                <div class="meta-grid">
                    {#if task.branch}
                        <div class="meta-item"><span class="label">Branch</span><span class="value">{task.branch}</span></div>
                    {/if}
                    {#if task.session_id}
                        <div class="meta-item"><span class="label">Session</span><span class="value">{task.session_id.slice(0, 8)}</span></div>
                    {/if}
                    {#if task.llama_server_url}
                        <div class="meta-item"><span class="label">Agent</span><span class="value">{task.llama_server_url}</span></div>
                    {/if}
                    {#if task.model_name}
                        <div class="meta-item"><span class="label">Model</span><span class="value">{task.model_name}</span></div>
                    {/if}
                    <div class="meta-item"><span class="label">Created</span><span class="value">{new Date(task.created_at).toLocaleString()}</span></div>
                    <div class="meta-item"><span class="label">Updated</span><span class="value">{new Date(task.updated_at).toLocaleString()}</span></div>
                </div>
            </div>
        </div>

        <!-- Footer Actions -->
        <div class="footer">
            {#if task.status === 'backlog'}
                <button class="btn-primary" onclick={handleStart}>Start</button>
            {/if}
            {#if task.status === 'in_progress'}
                {#if task.status_state === 'running'}
                    <button class="btn-danger" onclick={handleStop}>Stop</button>
                {/if}
                {#if task.status_state === 'paused'}
                    <button class="btn-primary" onclick={handleResume}>Resume</button>
                {/if}
                {#if task.status_state === 'failed'}
                    <button class="btn-primary" onclick={handleRetry}>Retry</button>
                {/if}
            {/if}
            {#if task.status === 'review'}
                <button class="btn-primary" onclick={() => handleMove('done')}>Done</button>
                <button class="btn-secondary" onclick={() => handleMove('in_progress')}>Reject</button>
            {/if}
            {#if task.status === 'done'}
                <button class="btn-secondary" onclick={() => handleMove('backlog')}>Restore</button>
            {/if}
            <button class="btn-danger" onclick={handleDelete}>Delete</button>
        </div>
    </div>
{/if}

<style>
    .overlay {
        position: fixed;
        inset: 0;
        background: rgba(0, 0, 0, 0.5);
        z-index: 99;
    }
    .card-detail {
        position: fixed;
        right: 0;
        top: 0;
        bottom: 0;
        width: 480px;
        max-width: 100vw;
        background: var(--color-surface-1);
        border-left: 1px solid var(--color-border);
        z-index: 100;
        display: flex;
        flex-direction: column;
        overflow: hidden;
    }
    .header {
        padding: 16px;
        border-bottom: 1px solid var(--color-border);
        flex-shrink: 0;
    }
    .title {
        font-size: 16px;
        font-weight: 600;
        cursor: pointer;
        margin: 0 0 8px;
    }
    .title-input {
        width: 100%;
        padding: 4px 8px;
        font-size: 16px;
        background: var(--color-surface-2);
        border: 1px solid var(--color-accent);
        border-radius: var(--radius-sm);
        color: var(--color-text-primary);
    }
    .header-right {
        display: flex;
        gap: 8px;
        align-items: center;
    }
    .badge {
        padding: 2px 8px;
        border-radius: var(--radius-sm);
        font-size: 12px;
        font-weight: 500;
    }
    .close-btn {
        background: none;
        border: none;
        color: var(--color-text-secondary);
        font-size: 16px;
        cursor: pointer;
        padding: 4px;
    }
    .body {
        flex: 1;
        overflow-y: auto;
        padding: 16px;
    }
    .section {
        margin-bottom: 16px;
    }
    .section h4 {
        font-size: 13px;
        font-weight: 600;
        color: var(--color-text-secondary);
        margin: 0 0 8px;
        display: flex;
        justify-content: space-between;
        align-items: center;
    }
    .prompt-text, .output-text {
        font-size: 14px;
        line-height: 1.5;
        color: var(--color-text-primary);
        white-space: pre-wrap;
    }
    .error-text {
        font-size: 14px;
        color: var(--color-status-red);
        white-space: pre-wrap;
    }
    .diff {
        background: var(--color-surface-0);
        padding: 12px;
        border-radius: var(--radius-md);
        font-size: 12px;
        font-family: var(--font-mono);
        overflow-x: auto;
        white-space: pre;
        margin: 0;
        max-height: 300px;
        overflow-y: auto;
    }
    .metadata .meta-grid {
        display: grid;
        grid-template-columns: 1fr 1fr;
        gap: 8px;
    }
    .meta-item {
        display: flex;
        flex-direction: column;
        gap: 2px;
    }
    .meta-item .label {
        font-size: 11px;
        color: var(--color-text-tertiary);
        text-transform: uppercase;
    }
    .meta-item .value {
        font-size: 13px;
        color: var(--color-text-primary);
        font-family: var(--font-mono);
    }
    .footer {
        padding: 12px 16px;
        border-top: 1px solid var(--color-border);
        display: flex;
        gap: 8px;
        flex-wrap: wrap;
        flex-shrink: 0;
    }
    .btn-primary {
        padding: 6px 16px;
        background: var(--color-accent);
        color: white;
        border-radius: var(--radius-md);
        font-size: 13px;
    }
    .btn-primary:hover {
        background: var(--color-accent-hover);
    }
    .btn-secondary {
        padding: 6px 16px;
        background: var(--color-surface-3);
        color: var(--color-text-primary);
        border-radius: var(--radius-md);
        font-size: 13px;
    }
    .btn-secondary:hover {
        background: var(--color-surface-4);
    }
    .btn-danger {
        padding: 6px 16px;
        background: var(--color-status-red);
        color: white;
        border-radius: var(--radius-md);
        font-size: 13px;
    }
    .btn-sm {
        padding: 4px 8px;
        background: var(--color-surface-3);
        color: var(--color-text-primary);
        border-radius: var(--radius-sm);
        font-size: 12px;
    }
</style>