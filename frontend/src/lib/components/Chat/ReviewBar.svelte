<script lang="ts">
    import type { Provider } from '$lib/types';
    import ModelSelector from './ModelSelector.svelte';

    let {
        taskStatus,
        showResumeButton,
        showRestartButton,
        isStarting,
        isCommitting,
        disabled,
        hasStagedChanges,
        queueStatus,
        providers,
        selectedModel,
        selectedUrl,
        selectedReasoningEffort,
        onMoveTask,
        onStartTask,
        onCommitAndDone,
        onMoveToDone,
        onModelSelect,
    }: {
        taskStatus: string;
        showResumeButton: boolean;
        showRestartButton: boolean;
        isStarting: boolean;
        isCommitting: boolean;
        disabled: boolean;
        hasStagedChanges: boolean;
        queueStatus: string | null;
        providers: Provider[];
        selectedModel: string;
        selectedUrl: string;
        selectedReasoningEffort: string;
        onMoveTask?: (status: string) => void;
        onStartTask: () => Promise<void>;
        onCommitAndDone: () => Promise<void>;
        onMoveToDone: () => Promise<void>;
        onModelSelect: (providerName: string, modelName: string, providerUrl: string, reasoningEffort?: string) => Promise<void>;
    } = $props();
</script>

{#if taskStatus === 'done' && onMoveTask}
    <div class="chat-review-bar">
        <button class="btn-move" onclick={() => onMoveTask('review')} title="Move task to Review">
            → Review
        </button>
        <button class="btn-move" onclick={() => onMoveTask('backlog')} title="Move task to Backlog">
            → Backlog
        </button>
    </div>
{/if}
{#if taskStatus === 'review'}
    <div class="chat-review-bar">
        {#if onMoveTask}
            <div class="move-buttons">
                <button class="btn-move" onclick={() => onMoveTask('done')} title="Move task to Done">
                    → Done
                </button>
                <button class="btn-move" onclick={() => onMoveTask('backlog')} title="Move task to Backlog">
                    → Backlog
                </button>
            </div>
        {/if}
        {#if showResumeButton}
            <button class="btn-commit-done" onclick={onStartTask} disabled={isStarting || disabled} title="Resume the task from the last user message">
                {#if isStarting}
                    <span class="spinner-inline"></span> Resuming...
                {:else}
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M4 2l10 6-10 6V2z" fill="currentColor"/></svg>
                    Resume
                {/if}
            </button>
        {:else if hasStagedChanges}
            <button class="btn-commit-done" onclick={onCommitAndDone} disabled={isCommitting || disabled} title="Commit changes and move task to Done">
                {#if queueStatus === 'queued'}
                    <span class="spinner-inline"></span> In queue...
                {:else if queueStatus === 'processing' || isCommitting}
                    <span class="spinner-inline"></span> Committing...
                {:else}
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M4 8l3 3 5-6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                    Commit and move to Done
                {/if}
            </button>
        {:else}
            <button class="btn-commit-done" onclick={onMoveToDone} disabled={isCommitting || disabled} title="Move task to Done">
                {#if isCommitting}
                    <span class="spinner-inline"></span> Moving...
                {:else}
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M4 8l3 3 5-6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                    Move to Done
                {/if}
            </button>
        {/if}
    </div>
{/if}
{#if showRestartButton}
    <div class="chat-review-bar">
        {#if providers.length > 0}
            <div class="restart-model-row">
                <ModelSelector
                    providers={providers}
                    currentModel={selectedModel}
                    currentUrl={selectedUrl}
                    currentReasoningEffort={selectedReasoningEffort}
                    onSelect={onModelSelect}
                />
            </div>
        {/if}
        <button class="btn-restart-task" onclick={onStartTask} disabled={isStarting || disabled || (providers.length > 0 && !selectedModel)} title="Restart the task from the last user message">
            {#if isStarting}
                <span class="spinner-inline"></span> Restarting...
            {:else}
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                    <path d="M2 8a6 6 0 0 1 10.47-4M14 8a6 6 0 0 1-10.47 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                    <path d="M12 1v3h-3M4 15v-3h3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
                Restart Task
            {/if}
        </button>
    </div>
{/if}

<style>
    .chat-review-bar {
        display: flex;
        align-items: center;
        gap: 8px;
        justify-content: flex-end;
        padding: 8px 12px;
        border-top: 1px solid var(--color-border);
        background: var(--color-surface-0);
        flex-shrink: 0;
    }
    .move-buttons {
        display: flex;
        align-items: center;
        gap: 8px;
        margin-right: auto;
    }
    .btn-move {
        display: inline-flex;
        align-items: center;
        padding: 6px 12px;
        border-radius: var(--radius-md);
        font-size: 0.8125rem;
        font-weight: 500;
        background: var(--color-surface-2);
        color: var(--color-text);
        border: 1px solid var(--color-border);
        cursor: pointer;
        transition: all 0.15s ease;
    }
    .btn-move:hover {
        background: var(--color-surface-1);
        border-color: var(--color-accent);
    }
    .btn-commit-done {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        padding: 7px 16px;
        border-radius: var(--radius-md);
        font-size: 0.8125rem;
        font-weight: 600;
        background: var(--color-surface-2);
        color: var(--color-text-primary);
        border: 1.5px solid var(--color-status-green);
        cursor: pointer;
        transition: all 0.15s ease;
    }
    .btn-commit-done:hover:not(:disabled) {
        background: var(--color-surface-3);
        opacity: 0.9;
    }
    .btn-commit-done:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }
    .restart-model-row {
        display: flex;
        align-items: center;
        margin-right: auto;
    }
    .btn-restart-task {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        padding: 8px 16px;
        border-radius: var(--radius-md);
        font-size: 0.8125rem;
        font-weight: 600;
        background: var(--color-accent);
        color: white;
        border: none;
        cursor: pointer;
        transition: all 0.15s ease;
    }
    .btn-restart-task:hover:not(:disabled) {
        opacity: 0.85;
    }
    .btn-restart-task:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }
    .spinner-inline {
        display: inline-block;
        width: 14px;
        height: 14px;
        border: 2px solid currentColor;
        border-top-color: transparent;
        border-radius: 50%;
        animation: spin 0.6s linear infinite;
    }
    @keyframes spin { to { transform: rotate(360deg); } }
</style>
