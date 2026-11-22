<script lang="ts">
    import type { Provider } from '$lib/types';
    import type { Extension } from '$lib/types';
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
        startingTurn = false,
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
        /** The development row this task's project owns, when it has one; nothing else shows Try Extension. */
        devExtension,
        onTryExtension,
        isTryingExtension = false,
        tryExtensionNotice = '',
    }: {
        taskStatus: string;
        taskExecutionState: string;
        showResumeButton: boolean;
        /** The click that asked the last question is still working. */
        startingTurn?: boolean;
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
        devExtension?: Extension | null;
        /** Load this task's bytes and report what came back; the text is the person's to relay. */
        onTryExtension?: () => Promise<void>;
        isTryingExtension?: boolean;
        /** The load's answer: the manifest's own failure, or the reloading sentence. */
        tryExtensionNotice?: string;
    } = $props();

    // Stopped mid-run: the task stays in the In Progress column, Resume continues the conversation from
    // where it left off, and Backlog sends it back with its branch and worktree intact. A fork stops the
    // run it branches from, so mid-handoff that stop is not a question waiting for a second start.
    let isPaused = $derived(isTaskPaused(taskExecutionState) && !startingTurn);
</script>

<!-- The four bars below share one stylesheet rule and are told apart by `data-bar`, which is what a reader
     that means one of them asks for: the class names all four. -->
<!-- A task of an authoring project: the version under review is loaded here, and the load's answer is
     the text the person carries back to the agent, so it is shown rather than flashed. -->
{#if taskStatus === 'review' && devExtension && onTryExtension}
    <div class="chat-review-bar" data-bar="dev">
        {#if tryExtensionNotice}
            <span class="try-notice" role="status">{tryExtensionNotice}</span>
        {/if}
        <button class="btn-commit-done btn-try-extension" onclick={onTryExtension} disabled={isTryingExtension} title={t('chat.review.tryExtensionTip')}>
            {#if isTryingExtension}
                <Spinner decorative /> {t('chat.review.tryExtensionLoading')}
            {:else}
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                    <path d="M2 8a6 6 0 0 1 10.47-4M14 8a6 6 0 0 1-10.47 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                    <path d="M12 1v3h-3M4 15v-3h3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                </svg>
                {t('chat.review.tryExtension')}
            {/if}
        </button>
    </div>
{/if}
{#if isPaused}
    <div class="chat-review-bar" data-bar="paused">
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
    <div class="chat-review-bar" data-bar="done">
        <button class="btn-move" onclick={() => onMoveTask('review')} title={t('chat.review.moveToReviewTip')}>
            {t('chat.review.moveToReview')}
        </button>
        <button class="btn-move" onclick={() => onMoveTask('backlog')} title={t('chat.review.moveToBacklogTip')}>
            {t('chat.review.moveToBacklog')}
        </button>
    </div>
{/if}
{#if taskStatus === 'review'}
    <div class="chat-review-bar" data-bar="review">
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
    <div class="chat-review-bar" data-bar="restart">
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
        transition: all var(--dur-base) ease;
    }
    .btn-move:hover {
        /* `opacity: 1` cancels app.css's hover dim the way `shared.css` explains: the background and
           border shift is this control's answer, and it must not also fade. */
        opacity: 1;
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
        transition: all var(--dur-base) ease;
    }
    .btn-commit-and-done {
        border-color: var(--color-status-yellow);
    }
    .btn-commit-done:hover:not(:disabled) {
        opacity: 1;
        background: var(--color-surface-3);
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
        color: var(--color-btn-text);
        border: none;
        transition: all var(--dur-base) ease;
    }
    .btn-restart-task:hover:not(:disabled) {
        opacity: 0.85;
    }
    /* The load's answer sits beside the button that asked for it: it is often a manifest complaint
       the person relays to the agent word for word. */
    .try-notice {
        margin-right: auto;
        font-size: var(--text-xs);
        color: var(--color-text-secondary);
        overflow-wrap: anywhere;
    }
    .btn-try-extension {
        border-color: var(--color-accent);
    }
</style>
