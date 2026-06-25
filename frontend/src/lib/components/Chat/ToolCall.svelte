<script lang="ts">
    import { getToolDisplayName } from '$lib/types';
    import shieldGreenIcon from '$lib/assets/icons/shield-green.svg';
    import shieldOffIcon from '$lib/assets/icons/shield-off.svg';
    import zapIcon from '$lib/assets/icons/zap.svg';
    import fileTextIcon from '$lib/assets/icons/file-text.svg';
    import editIcon from '$lib/assets/icons/edit.svg';
    import checkCircleIcon from '$lib/assets/icons/check-circle.svg';
    import { formatToolArgs, HIDE_ARGS_FOR_TOOLS, SANDBOXABLE_TOOLS, ARGS_MAX_LEN, EDIT_DIFF_MAX_LINES } from '$lib/utils/toolArgs';

    interface Props {
        toolCall: {
            toolId: string;
            toolName: string;
            displayName?: string;
            args: Record<string, unknown>;
            result?: string;
            success?: boolean;
            isPending?: boolean;
            sandbox?: boolean;
            duration?: number;
        };
        expandedArgs: Set<string>;
        onToggleArgs: (toolId: string) => void;
        onNavigateTask?: (taskId: string) => void;
    }

    let { toolCall: tc, expandedArgs, onToggleArgs, onNavigateTask }: Props = $props();

    function truncateArgs(display: string): { text: string; truncated: boolean } {
        if (display.length <= ARGS_MAX_LEN) return { text: display, truncated: false };
        return { text: display.slice(0, ARGS_MAX_LEN) + '…', truncated: true };
    }

    let showArgs = $derived(!HIDE_ARGS_FOR_TOOLS.has(tc.toolName));
    let argsDisplay = $derived(formatToolArgs(tc.toolName, tc.args));
    let truncated = $derived(truncateArgs(argsDisplay));

    // Icon selection
    let iconSrc = $derived(
        tc.toolName === 'read_file' ? fileTextIcon :
        tc.toolName === 'write_file' || tc.toolName === 'edit_file' ? editIcon :
        tc.toolName === 'task_complete' ? checkCircleIcon :
        zapIcon
    );

    // Parse create_task result
    let createResult = $derived(
        tc.toolName === 'create_task' && tc.result ?
        (() => { try { return JSON.parse(tc.result); } catch { return null; } })() :
        null
    );

    interface EditDiffLine { type: 'removed' | 'added'; text: string; }
    interface EditDiffBlock { edits: EditDiffLine[]; separator?: boolean; }

    function formatEditDiff(edits: Array<{ oldText: string; newText: string }> | undefined): EditDiffBlock[] {
        if (!edits) return [];
        const blocks: EditDiffBlock[] = [];
        for (const edit of edits) {
            const lines: EditDiffLine[] = [];
            const oldLines = edit.oldText.split('\n');
            const newLines = edit.newText.split('\n');
            const maxLines = EDIT_DIFF_MAX_LINES;
            for (const line of oldLines.slice(0, maxLines)) {
                lines.push({ type: 'removed', text: line });
            }
            for (const line of newLines.slice(0, maxLines)) {
                lines.push({ type: 'added', text: line });
            }
            blocks.push({ edits: lines, separator: true });
        }
        return blocks;
    }
</script>

<div class="tool-call-entry">
    <div class="tool-call-card" style="border-left-color: {tc.isPending ? 'var(--color-accent)' : (tc.success ? 'var(--color-status-green)' : 'var(--color-status-red)')}">
        <div class="tool-call-header">
            {#if tc.sandbox}
                <img class="tool-sandbox-icon" src={shieldGreenIcon} width="14" height="14" alt="" title="Sandboxed" />
            {:else if SANDBOXABLE_TOOLS.has(tc.toolName)}
                <img class="tool-sandbox-icon" src={shieldOffIcon} width="14" height="14" alt="" title="Not sandboxed" />
            {/if}
            {#if tc.toolName === 'create_task'}
                <svg class="tool-icon" width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                    <rect x="2" y="2" width="12" height="12" rx="2"/>
                    <path d="M5 5h1M5 7.5h1M5 10h1"/>
                    <path d="M8 7.5l1 1 2-2"/>
                    <path d="M8 11l1 1 2-2"/>
                </svg>
            {:else}
                <img class="tool-icon" width="14" height="14" alt="" src={iconSrc} />
            {/if}
            <span class="tool-name" title={tc.toolName}>{getToolDisplayName(tc.displayName, tc.toolName)}</span>
            {#if showArgs}
                {#if expandedArgs.has(tc.toolId)}
                    <button class="tool-args-link" onclick={() => onToggleArgs(tc.toolId)} aria-label="Collapse arguments">
                        <pre class="tool-args tool-args-expanded">{argsDisplay}</pre>
                    </button>
                {:else}
                    {#if truncated.truncated}
                        <button class="tool-args-link" onclick={() => onToggleArgs(tc.toolId)} aria-label="Expand arguments">
                            <pre class="tool-args tool-args-collapsed">{truncated.text}</pre>
                        </button>
                    {:else}
                        <pre class="tool-args">{argsDisplay}</pre>
                    {/if}
                {/if}
            {/if}
            {#if tc.isPending}
                <span class="tool-status pending"><span class="spinner"></span> Running...</span>
            {:else if !tc.success}
                <span class="tool-status error">Failed</span>
            {/if}
        </div>
        {#if tc.toolName === 'edit_file' && tc.args.edits}
            <div class="edit-diff">
                {#each formatEditDiff(tc.args.edits as Array<{ oldText: string; newText: string }>) as block}
                    {#each block.edits as line}
                        <div class="edit-diff-line {line.type === 'removed' ? 'edit-removed' : 'edit-added'}">
                            <span class="edit-diff-sign">{line.type === 'removed' ? '-' : '+'}</span>
                            <span class="edit-diff-text">{line.text}</span>
                        </div>
                    {/each}
                    {#if block.separator}
                        <div class="edit-separator"></div>
                    {/if}
                {/each}
            </div>
            {#if !tc.success && tc.result && tc.result.trim()}
                <pre class="tool-result tool-result-error">{tc.result}</pre>
            {/if}
        {:else if tc.toolName === 'create_task' && tc.result && tc.success}
            {#if createResult && createResult.task_id}
                <div class="tool-result tool-create-task">
                    <span class="create-task-text">Created task</span>
                    <button class="btn-open-task" onclick={() => onNavigateTask?.(createResult.task_id as string)} title="Open task">
                        <span class="btn-open-task-text">{createResult.title as string}</span>
                    </button>
                </div>
            {:else}
                <pre class="tool-result" class:tool-result-error={!tc.success}>{tc.result}</pre>
            {/if}
        {:else if tc.toolName === 'task_complete' && tc.args.summary}
            <pre class="tool-result tool-result-summary">{tc.args.summary as string}</pre>
        {:else if tc.result && tc.result.trim()}
            <pre class="tool-result" class:tool-result-error={!tc.success}>{tc.result}</pre>
        {/if}
        {#if tc.duration !== undefined}
            <span class="tool-duration">{tc.duration}ms</span>
        {/if}
    </div>
</div>

<style>
    .tool-call-entry { padding: 4px 0; }
    .tool-call-card {
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-left: 3px solid var(--color-accent);
        border-radius: var(--radius-md);
        padding: 8px;
    }
    .tool-call-header { display: flex; align-items: center; gap: 6px; margin-bottom: 4px; }
    .tool-name {
        font-size: 0.8125rem;
        font-weight: 600;
        color: var(--color-text-primary);
    }
    .tool-args {
        font-size: 0.75rem;
        color: var(--color-text-secondary);
        margin: 0;
        padding: 0;
        font-family: var(--font-mono);
        overflow-x: auto;
    }
    .tool-args-collapsed {
        max-width: 300px;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }
    .tool-args-link {
        background: none;
        border: none;
        padding: 0;
        cursor: pointer;
        color: inherit;
        font-family: inherit;
        text-align: left;
        width: 100%;
    }
    .tool-status {
        font-size: 0.75rem;
        margin-left: auto;
        display: flex;
        align-items: center;
        gap: 4px;
    }
    .tool-status.pending { color: var(--color-accent); }
    .tool-status.error { color: var(--color-status-red); }
    .spinner {
        display: inline-block;
        width: 10px;
        height: 10px;
        border: 2px solid currentColor;
        border-right-color: transparent;
        border-radius: 50%;
        animation: tool-spin 0.6s linear infinite;
    }
    @keyframes tool-spin { to { transform: rotate(360deg); } }
    .tool-result {
        background: var(--color-surface-0);
        padding: 6px 8px;
        border-radius: var(--radius-sm);
        font-size: 0.75rem;
        font-family: var(--font-mono);
        overflow-x: auto;
        margin: 4px 0 0;
        max-height: 120px;
        overflow-y: auto;
        white-space: pre-wrap;
        word-break: break-word;
    }
    .tool-result-error { color: var(--color-status-red); }
    .tool-result-summary {
        font-family: inherit;
        color: var(--color-text-secondary);
        font-style: italic;
    }
    .tool-create-task {
        display: flex;
        align-items: center;
        gap: 6px;
        background: var(--color-surface-0);
        padding: 6px 8px;
        border-radius: var(--radius-sm);
        margin: 4px 0 0;
    }
    .create-task-text {
        color: var(--color-text-secondary);
        font-size: 0.75rem;
    }
    .btn-open-task {
        display: flex;
        align-items: center;
        gap: 6px;
        padding: 6px 10px;
        border-radius: var(--radius-md);
        font-size: 0.75rem;
        font-weight: 500;
        background: var(--color-surface-2);
        color: var(--color-text-primary);
        border: 1px solid var(--color-border);
        border-left: 3px solid var(--color-column-backlog);
        cursor: pointer;
        transition: all 0.15s ease;
        max-width: 100%;
        overflow: hidden;
        font-family: inherit;
        flex-shrink: 0;
    }
    .btn-open-task-text {
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .btn-open-task:hover {
        border-color: var(--color-border-bright);
        border-left-color: var(--color-column-backlog);
        background: var(--color-surface-3);
    }
    .tool-duration {
        font-size: 0.6875rem;
        color: var(--color-text-tertiary);
        margin-top: 4px;
        display: block;
    }
    .edit-diff {
        font-family: var(--font-mono);
        font-size: 0.75rem;
        line-height: 1.4;
        overflow-x: auto;
    }
    .edit-diff-line {
        display: flex;
        gap: 0;
        padding: 0 8px;
        border-radius: 2px;
        white-space: pre-wrap;
        word-break: break-word;
    }
    .edit-removed {
        background: rgba(248,81,73,0.12);
        color: var(--color-text-secondary);
    }
    .edit-added {
        background: rgba(63,185,80,0.12);
        color: var(--color-text-secondary);
    }
    .edit-diff-sign {
        flex-shrink: 0;
        width: 1em;
        user-select: none;
        font-weight: 600;
    }
    .edit-removed .edit-diff-sign { color: rgba(248,81,73,0.8); }
    .edit-added .edit-diff-sign { color: rgba(63,185,80,0.8); }
    .edit-diff-text {
        flex: 1;
        min-width: 0;
    }
    .edit-separator {
        border-top: 1px dashed var(--color-border);
        margin: 4px 8px;
    }
</style>
