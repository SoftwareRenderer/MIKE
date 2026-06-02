<script lang="ts">
    import { createTask } from '$lib/api';
    import { board as boardStore, showCreateTask } from '$lib/store';
    import { createEventDispatcher } from 'svelte';

    export let projectId: string | undefined;

    let title = $state('');
    let prompt = $state('');
    let autoCommit = $state(false);
    let autoPr = $state(false);
    let loading = $state(false);
    let error = $state('');

    const dispatch = createEventDispatcher();

    async function handleSubmit() {
        if (!projectId || !title.trim() || !prompt.trim()) return;
        loading = true;
        error = '';
        try {
            const task = await createTask(projectId, {
                title: title.trim(),
                prompt: prompt.trim(),
                auto_commit: autoCommit,
                auto_pr: autoPr,
            });
            const board = boardStore.get();
            if (board) {
                board.tasks.push(task);
                boardStore.set(board);
            }
            showCreateTask.set(false);
        } catch (e) {
            error = String(e);
        } finally {
            loading = false;
        }
    }

    function handleClose() {
        dispatch('close');
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
                Title
                <input type="text" bind:value={title} placeholder="Task title" maxlength="200" required />
            </label>
            <label>
                Prompt
                <textarea bind:value={prompt} placeholder="Describe the task the AI should execute..." rows="5" required></textarea>
            </label>
            <div class="options">
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
                <button type="submit" class="btn-primary" disabled={loading || !title.trim() || !prompt.trim()}>
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
</style>