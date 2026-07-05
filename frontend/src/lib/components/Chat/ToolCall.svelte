<script lang="ts">
    import { getToolDisplayName } from '$lib/types';
    import shieldGreenIcon from '$lib/assets/icons/shield-green.svg';
    import shieldOffIcon from '$lib/assets/icons/shield-off.svg';
    import zapIcon from '$lib/assets/icons/zap.svg';
    import fileTextIcon from '$lib/assets/icons/file-text.svg';
    import editIcon from '$lib/assets/icons/edit.svg';
    import checkCircleIcon from '$lib/assets/icons/check-circle.svg';
    import { formatToolArgs, HIDE_ARGS_FOR_TOOLS, SANDBOXABLE_TOOLS, ARGS_MAX_LEN, EDIT_DIFF_MAX_LINES } from '$lib/utils/toolArgs';
    import { getTaskMessageFull } from '$lib/api';

    interface Props {
        toolCall: {
            toolId: string;
            toolName: string;
            displayName?: string;
            args: Record<string, unknown>;
            raw_args?: string;
            path?: string;
            result?: string;
            success?: boolean;
            isPending?: boolean;
            sandbox?: boolean;
            duration?: number;
            bytesWritten?: number;
            resultTruncated?: boolean; // true if backend truncated the result
            originalLines?: number; // original line count before truncation
            originalBytes?: number; // original byte count before truncation
            messageId?: string; // tool_call message ID for fetching full result
        };
        expandedArgs: Set<string>;
        projectId?: string;
        taskId?: string;
        onToggleArgs: (toolId: string) => void;
        onNavigateTask?: (taskId: string) => void;
    }

    let { toolCall: tc, expandedArgs, onToggleArgs, onNavigateTask, projectId, taskId }: Props = $props();

    // For bash: distinguish streaming (command being generated) from executing (command running).
    // Streaming: raw_args is partial JSON. Executing: raw_args is complete JSON (or args.command set).
    let isBashExecuting = $derived(
        tc.toolName === 'bash' && tc.isPending && (
            !!tc.args.command ||
            (tc.raw_args && (() => { try { JSON.parse(tc.raw_args); return true; } catch { return false; } })())
        )
    );

    function truncateArgs(display: string): { text: string; truncated: boolean } {
        if (display.length <= ARGS_MAX_LEN) return { text: display, truncated: false };
        return { text: display.slice(0, ARGS_MAX_LEN) + '…', truncated: true };
    }

    function formatBytes(bytes: number): string {
        if (bytes < 1024) return bytes + 'B';
        if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + 'KB';
        return (bytes / (1024 * 1024)).toFixed(1) + 'MB';
    }

    let showArgs = $derived(!HIDE_ARGS_FOR_TOOLS.has(tc.toolName));
    let argsDisplay = $derived(formatToolArgs(tc.toolName, tc.args, tc.raw_args));
    let truncated = $derived(truncateArgs(argsDisplay));

    // Bash result truncation: max 10 lines AND max 1KB (whichever is smaller)
    const RESULT_MAX_LINES = 10;
    const RESULT_MAX_BYTES = 1024;
    let resultExpanded = $state(false);
    let resultLoading = $state(false);
    let resolvedResult = $state<string | undefined>();

    /** Effective result: resolved (fetched) value, or the prop value. */
    let effectiveResult = $derived(resolvedResult ?? tc.result ?? '');

    // Bash actively streaming — no truncation, auto-scroll enabled.
    let isBashStreaming = $derived(
        tc.toolName === 'bash' && tc.isPending && effectiveResult && effectiveResult.length > 0
    );

    let scrollEl: HTMLPreElement | null = null;
    let userScrolledUp = $state(false);
    let prevResultLen = $state(0);

    // Bash tools streamed this session — never truncated (array for reactivity).
    let streamedBashToolIds = $state<string[]>([]);
    let prevIsPending = $state<boolean | undefined>();

    $effect(() => {
        if (tc.toolName !== 'bash') return;
        const pending = tc.isPending;
        if (prevIsPending === true && pending === false) {
            streamedBashToolIds = [...streamedBashToolIds, tc.toolId];
        }
        prevIsPending = pending;
    });

    /** Scroll to bottom of result window. */
    function scrollToBottom() {
        if (scrollEl) {
            scrollEl.scrollTop = scrollEl.scrollHeight;
        }
    }

    /** Check if user is near the bottom (within 60px). */
    function isNearBottom(el: HTMLPreElement): boolean {
        return el.scrollHeight - el.scrollTop - el.clientHeight < 60;
    }

    function handleResultScroll() {
        if (scrollEl) {
            userScrolledUp = !isNearBottom(scrollEl);
        }
    }

    $effect(() => {
        if (!isBashStreaming) return;
        const len = effectiveResult.length;
        if (len > prevResultLen) {
            if (!userScrolledUp) {
                requestAnimationFrame(scrollToBottom);
            }
            prevResultLen = len;
        }
    });

    function getTruncatedResult(text: string): string {
        if (!text) return '';
        const lines = text.split('\n');
        // Find how many lines fit within 1KB
        let bytesAt1KB = 0;
        let byteTotal = 0;
        for (let i = 0; i < lines.length; i++) {
            byteTotal += lines[i].length + (i > 0 ? 1 : 0);
            if (byteTotal > RESULT_MAX_BYTES) {
                bytesAt1KB = i;
                break;
            }
            bytesAt1KB = i + 1;
        }
        const maxLines = Math.min(RESULT_MAX_LINES, bytesAt1KB);
        if (maxLines >= lines.length && text.length <= RESULT_MAX_BYTES) return text + '…';
        if (maxLines > 1) {
            return lines.slice(0, maxLines - 1).join('\n') + '\n…';
        }
        return text.slice(0, RESULT_MAX_BYTES) + '…';
    }

    let bashResultNeedsTruncation = $derived(
        tc.toolName === 'bash' && !isBashStreaming && !streamedBashToolIds.includes(tc.toolId) && effectiveResult && (
            tc.resultTruncated || effectiveResult.split('\n').length > RESULT_MAX_LINES || effectiveResult.length > RESULT_MAX_BYTES
        )
    );
    // Use original stats from backend when available, otherwise compute from current result
    let bashResultStats = $derived({
        fullLength: tc.originalBytes ?? (effectiveResult ? effectiveResult.length : 0),
        lineCount: tc.originalLines ?? (effectiveResult ? effectiveResult.split('\n').length : 0),
    });

    async function handleResultToggle() {
        if (resultExpanded) {
            resultExpanded = false;
            return;
        }
        // If result was truncated by backend, fetch full message
        if (tc.resultTruncated && tc.messageId && projectId && taskId && !resolvedResult) {
            resultLoading = true;
            try {
                const fullMsg = await getTaskMessageFull(projectId, taskId, tc.messageId);
                if (fullMsg.tool_result) {
                    const resultData = JSON.parse(fullMsg.tool_result);
                    if (resultData.output) {
                        resolvedResult = resultData.output;
                    }
                }
            } catch (e) {
                console.error('Failed to fetch full tool result:', e);
            } finally {
                resultLoading = false;
            }
        }
        resultExpanded = true;
    }

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
                <span class="tool-status pending"><span class="spinner"></span><span class="progress-text">{#if tc.toolName === 'task_complete'}Writing Summary{:else if tc.toolName === 'bash' && isBashExecuting}Running{#if tc.path}: {tc.path}{/if}{:else}Writing{#if tc.path}: {tc.path}{/if}{#if tc.bytesWritten !== undefined}: {formatBytes(tc.bytesWritten)}{/if}{/if}</span></span>
            {:else if !tc.success}
                <span class="tool-status error">Failed</span>
            {/if}
        </div>
        {#if tc.toolName === 'edit_file' && tc.args && tc.args.edits}
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
        {:else if tc.toolName === 'task_complete' && tc.args && tc.args.summary}
            <div class="tool-result tool-result-summary">{tc.args.summary as string}</div>
        {:else if tc.result && tc.result.trim()}
            {#if bashResultNeedsTruncation}
                <div class="tool-result-wrapper">
                    <pre class="tool-result" class:tool-result-error={!tc.success} class:tool-result-expanded={resultExpanded} bind:this={scrollEl} onscroll={handleResultScroll}>{#if resultLoading}Loading...{:else}{resultExpanded ? effectiveResult : getTruncatedResult(effectiveResult)}{/if}</pre>
                    <button class="tool-result-expand" onclick={() => handleResultToggle()} aria-expanded={resultExpanded} disabled={resultLoading}>
                        {resultLoading ? 'Loading...' : (resultExpanded ? 'Collapse' : `Show all (${bashResultStats.lineCount} lines, ${bashResultStats.fullLength > 1024 ? (bashResultStats.fullLength / 1024).toFixed(1) + 'KB' : bashResultStats.fullLength + 'B'})`)}
                    </button>
                </div>
            {:else}
                <pre class="tool-result" class:tool-result-error={!tc.success} bind:this={scrollEl} onscroll={handleResultScroll}>{effectiveResult}</pre>
            {/if}
        {/if}
        {#if tc.duration !== undefined && tc.toolName !== 'task_complete'}
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
    .tool-call-header { display: flex; align-items: center; gap: 6px; margin-bottom: 8px; }
    .tool-name {
        font-size: 0.8125rem;
        font-weight: 600;
        color: var(--color-text-primary);
        display: flex;
        align-items: center;
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
        display: flex;
        align-items: center;
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
    .progress-text { font-size: 0.8125rem; font-weight: 600; }
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
    .tool-result-expanded {
        max-height: none;
        overflow-y: visible;
    }
    .tool-result-error { color: var(--color-status-red); }
    .tool-result-wrapper { position: relative; }
    .tool-result-expand {
        display: inline-flex;
        align-items: center;
        background: none;
        border: none;
        color: var(--color-text-tertiary);
        font-size: 0.6875rem;
        cursor: pointer;
        padding: 2px 4px;
        margin: 0 0 4px;
        font-family: var(--font-mono);
        transition: color 0.15s ease;
    }
    .tool-result-expand:hover {
        color: var(--color-text-primary);
    }
    .tool-result-expand:disabled {
        opacity: 0.5;
        cursor: wait;
    }
    .tool-result-summary {
        font-family: inherit;
        font-size: 1rem;
        line-height: 1.875rem;
        color: var(--color-text-primary);
        white-space: pre-wrap;
        word-break: break-word;
        max-height: none;
        overflow-y: visible;
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
