<script lang="ts">
    import type { Provider } from '$lib/types';
    import Icon from '$lib/components/Icon.svelte';
    import Spinner from '$lib/components/Spinner.svelte';
    import commitPushIcon from '$lib/assets/icons/commit-push.svg?raw';
    import ModelSelector from './ModelSelector.svelte';
    import { isTaskPaused } from '$lib/utils/execution-state';
    import { t } from '$lib/i18n';

    let {
        taskStatus,
        taskExecutionState,
        showResumeButton,
        showRestartButton,
        isStarting,
        isCommitting,
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
        taskExecutionState: string;
        showResumeButton: boolean;
        showRestartButton: boolean;
        isStarting: boolean;
        isCommitting: boolean;
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

    // Stopped mid-run: the task stays in the In Progress column, Resume continues the conversation from
    // where it left off, and Backlog sends it back with its branch and worktree intact.
    let isPaused = $derived(isTaskPaused(taskExecutionState));
</script>

{#if isPaused}
    <div class="chat-review-bar">
        {#if onMoveTask}
            <div class="move-buttons">
                <button class="btn-move" onclick={() => onMoveTask('backlog')} title={t('chat.review.moveToBacklogTip')}>
                    {t('chat.review.moveToBacklog')}
                </button>
            </div>
        {/if}
        <button class="btn-commit-done" onclick={onStartTask} disabled={isStarting} title={t('chat.review.resumePausedTip')}>
            {#if isStarting}
                <Spinner decorative /> {t('chat.review.resuming')}
            {:else}
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M4 2l10 6-10 6V2z" fill="currentColor"/></svg>
                {t('chat.review.resume')}
            {/if}
        </button>
    </div>
{/if}
{#if taskStatus === 'done' && onMoveTask}
    <div class="chat-review-bar">
        <button class="btn-move" onclick={() => onMoveTask('review')} title={t('chat.review.moveToReviewTip')}>
            {t('chat.review.moveToReview')}
        </button>
        <button class="btn-move" onclick={() => onMoveTask('backlog')} title={t('chat.review.moveToBacklogTip')}>
            {t('chat.review.moveToBacklog')}
        </button>
    </div>
{/if}
{#if taskStatus === 'review'}
    <div class="chat-review-bar">
        {#if onMoveTask}
            <div class="move-buttons">
                <button class="btn-move" onclick={() => onMoveTask('done')} title={t('chat.review.moveToDoneTip')}>
                    {t('chat.review.moveToDone')}
                </button>
                <button class="btn-move" onclick={() => onMoveTask('backlog')} title={t('chat.review.moveToBacklogTip')}>
                    {t('chat.review.moveToBacklog')}
                </button>
            </div>
        {/if}
        {#if showResumeButton}
            <button class="btn-commit-done" onclick={onStartTask} disabled={isStarting} title={t('chat.review.resumeFromMessageTip')}>
                {#if isStarting}
                    <Spinner decorative /> {t('chat.review.resuming')}
                {:else}
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M4 2l10 6-10 6V2z" fill="currentColor"/></svg>
                    {t('chat.review.resume')}
                {/if}
            </button>
        {:else if hasStagedChanges}
            <button class="btn-commit-done btn-commit-and-done" onclick={onCommitAndDone} disabled={isCommitting} title={t('chat.review.commitAndDoneTip')}>
                {#if queueStatus === 'queued'}
                    <Spinner decorative /> {t('chat.review.inQueue')}
                {:else if queueStatus === 'processing' || isCommitting}
                    <Spinner decorative /> {t('chat.review.committing')}
                {:else}
                    <Icon src={commitPushIcon} color="currentColor" width={14} height={14} />
                    {t('chat.review.commitAndDone')}
                {/if}
            </button>
        {:else}
            <button class="btn-commit-done" onclick={onMoveToDone} disabled={isCommitting} title={t('chat.review.moveToDoneTip')}>
                {#if isCommitting}
                    <Spinner decorative /> {t('chat.review.moving')}
                {:else}
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M4 8l3 3 5-6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                    {t('chat.review.moveToDoneButton')}
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
        <button class="btn-restart-task" onclick={onStartTask} disabled={isStarting || (providers.length > 0 && !selectedModel)} title={t('chat.review.restartTip')}>
            {#if isStarting}
                <Spinner decorative /> {t('chat.review.restarting')}
            {:else}
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                    <path d="M2 8a6 6 0 0 1 10.47-4M14 8a6 6 0 0 1-10.47 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                    <path d="M12 1v3h-3M4 15v-3h3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
                {t('chat.review.restart')}
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
        font-size: var(--text-sm);
        font-weight: 500;
        background: var(--color-surface-2);
        color: var(--color-text-primary);
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
        font-size: var(--text-sm);
        font-weight: 600;
        background: var(--color-surface-2);
        color: var(--color-text-primary);
        border: 1.5px solid var(--color-status-green);
        cursor: pointer;
        transition: all 0.15s ease;
    }
    .btn-commit-and-done {
        border-color: var(--color-status-yellow);
    }
    .btn-commit-done:hover:not(:disabled) {
        background: var(--color-surface-3);
        opacity: 0.9;
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
        font-size: var(--text-sm);
        font-weight: 600;
        background: var(--color-accent-dark);
        color: white;
        border: none;
        cursor: pointer;
        transition: all 0.15s ease;
    }
    .btn-restart-task:hover:not(:disabled) {
        opacity: 0.85;
    }
</style>
