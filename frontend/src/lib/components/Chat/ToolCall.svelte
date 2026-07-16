<script lang="ts">
    import { marked } from 'marked';
    import { getToolDisplayName } from '$lib/types';
    import { slide } from 'svelte/transition';
    import Icon from '$lib/components/Icon.svelte';
    import shieldGreenIcon from '$lib/assets/icons/shield-green.svg?raw';
    import shieldOffIcon from '$lib/assets/icons/shield-off.svg?raw';
    import zapIcon from '$lib/assets/icons/zap.svg?raw';
    import fileTextIcon from '$lib/assets/icons/file-text.svg?raw';
    import editIcon from '$lib/assets/icons/edit.svg?raw';
    import checkCircleIcon from '$lib/assets/icons/check-circle.svg?raw';
    import questionIcon from '$lib/assets/icons/question.svg?raw';
    import { formatToolArgs, HIDE_ARGS_FOR_TOOLS, SANDBOXABLE_TOOLS, ARGS_MAX_LEN, EDIT_DIFF_MAX_LINES } from '$lib/utils/toolArgs';
    import { formatElapsed } from '$lib/utils/format-time';
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
            linesAdded?: number;
            linesRemoved?: number;
            resultTruncated?: boolean; // true if backend truncated the result
            originalLines?: number; // original line count before truncation
            originalBytes?: number; // original byte count before truncation
            messageId?: string; // tool_call message ID for fetching full result
            startedAt?: number; // timestamp when tool call started (ms)
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

    // Elapsed time for pending tool calls — updates every second via timer.
    let elapsedMs = $state(0);
    let timerRef = $state<ReturnType<typeof setInterval> | null>(null);

    $effect(() => {
        const pending = tc.isPending;
        if (pending && tc.startedAt) {
            const startedAt = tc.startedAt;
            elapsedMs = Date.now() - startedAt;
            if (timerRef === null) {
                timerRef = setInterval(() => {
                    elapsedMs = Date.now() - startedAt;
                }, 1000);
            }
        } else {
            if (timerRef !== null) {
                clearInterval(timerRef);
                timerRef = null;
            }
            elapsedMs = 0;
        }
        return () => {
            if (timerRef !== null) {
                clearInterval(timerRef);
                timerRef = null;
            }
        };
    });

    let elapsedText = $derived(
        tc.isPending && tc.startedAt && elapsedMs > 5000
            ? `Running for ${formatElapsed(elapsedMs)}`
            : ''
    );

    function truncateArgs(display: string): { text: string; truncated: boolean } {
        if (display.length <= ARGS_MAX_LEN) return { text: display, truncated: false };
        return { text: display.slice(0, ARGS_MAX_LEN) + '…', truncated: true };
    }

    // Track text selection so clicking to select doesn't toggle expanded args
    let selectionStart = $state(0);
    let wasSelecting = $state(false);

    function handleArgsMouseDown() {
        selectionStart = window.getSelection()?.toString().length ?? 0;
        wasSelecting = false;
    }

    function handleArgsMouseUp() {
        const selLen = window.getSelection()?.toString().length ?? 0;
        if (selLen !== selectionStart) wasSelecting = true;
    }

    function handleArgsClick() {
        if (!wasSelecting) onToggleArgs(tc.toolId);
        wasSelecting = false;
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

    // svelte-ignore non_reactive_update — DOM ref via bind:this
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

    let resultNeedsTruncation = $derived(
        (tc.toolName === 'bash' || tc.toolName === 'read_file') && effectiveResult && (
            tc.toolName !== 'bash' || (!isBashStreaming && !streamedBashToolIds.includes(tc.toolId))
        ) && (
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
    let iconInfo = $derived(
        tc.toolName === 'read_file' ? { src: fileTextIcon, color: '#E6EDF3' } :
        tc.toolName === 'write_file' || tc.toolName === 'edit_file' ? { src: editIcon, color: '#E6EDF3' } :
        tc.toolName === 'request_review' ? { src: checkCircleIcon, color: '#E6EDF3' } :
        tc.toolName === 'ask_question' ? { src: questionIcon, color: '#fff' } :
        { src: zapIcon, color: '#E6EDF3' }
    );

    // Parse create_task result
    let createResult = $derived(
        tc.toolName === 'create_task' && tc.result ?
        (() => { try { return JSON.parse(tc.result); } catch { return null; } })() :
        null
    );

    // Parse edit_task result
    let editResult = $derived(
        tc.toolName === 'edit_task' && tc.result ?
        (() => { try { return JSON.parse(tc.result); } catch { return null; } })() :
        null
    );

    // Parse view_tasks result
    let viewTasksResult = $derived(
        tc.toolName === 'view_tasks' && tc.result ?
        (() => { try { const arr = JSON.parse(tc.result); return Array.isArray(arr) ? arr : null; } catch { return null; } })() :
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

    // Edit diff expand/collapse
    let editDiffExpanded = $state(false);
    let editLinesAdded = $derived(() => {
        const edits = tc.args?.edits as Array<{ oldText: string; newText: string }> | undefined;
        if (!edits) return 0;
        let count = 0;
        for (const edit of edits) {
            count += (edit.newText || '').split('\n').length;
        }
        return count;
    });
    let editLinesRemoved = $derived(() => {
        const edits = tc.args?.edits as Array<{ oldText: string; newText: string }> | undefined;
        if (!edits) return 0;
        let count = 0;
        for (const edit of edits) {
            count += (edit.oldText || '').split('\n').length;
        }
        return count;
    });
    let hasEditDiff = $derived(tc.toolName === 'edit_file' && tc.args && tc.args.edits);

    // Rendered markdown for request_review summary
    let renderedSummaryHtml = $derived(
        tc.toolName === 'request_review' && tc.args && tc.args.summary
            ? (marked.parse(tc.args.summary as string, { async: false }) as string)
            : ''
    );

    // Rendered markdown for ask_question
    let renderedQuestionHtml = $derived(
        tc.toolName === 'ask_question' && tc.args && tc.args.question
            ? (marked.parse(tc.args.question as string, { async: false }) as string)
            : ''
    );
</script>

<div class="tool-call-entry">
    <div class="tool-call-card" style="border-left-color: {tc.isPending ? 'var(--color-accent)' : (tc.success ? 'var(--color-status-green)' : 'var(--color-status-red)')}">
        <div class="tool-call-header">
            {#if tc.sandbox}
                <Icon class="tool-sandbox-icon" src={shieldGreenIcon} color="#3FB950" width={14} height={14} title="Sandboxed" />
            {:else if SANDBOXABLE_TOOLS.has(tc.toolName)}
                <Icon class="tool-sandbox-icon" src={shieldOffIcon} color="#F85149" width={14} height={14} title="Not sandboxed" />
            {/if}
            {#if tc.toolName === 'create_task' || tc.toolName === 'edit_task'}
                <svg class="tool-icon" width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                    <rect x="2" y="2" width="12" height="12" rx="2"/>
                    <path d="M5 5h1M5 7.5h1M5 10h1"/>
                    <path d="M8 7.5l1 1 2-2"/>
                    <path d="M8 11l1 1 2-2"/>
                </svg>
            {:else if tc.toolName === 'view_tasks'}
                <svg class="tool-icon" width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                    <rect x="2" y="2" width="12" height="4" rx="1"/>
                    <rect x="2" y="8" width="12" height="4" rx="1"/>
                </svg>
            {:else}
                <Icon class="tool-icon" src={iconInfo.src} color={iconInfo.color} width={14} height={14} />
            {/if}
            <span class="tool-name" title={tc.toolName}>{getToolDisplayName(tc.displayName, tc.toolName)}</span>
            {#if showArgs}
                {#if expandedArgs.has(tc.toolId)}
                    <button class="tool-args-link" onclick={handleArgsClick} onmousedown={handleArgsMouseDown} onmouseup={handleArgsMouseUp} aria-label="Collapse arguments">
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
                <span class="tool-status pending"><span class="spinner"></span><span class="progress-text">{#if elapsedText}{elapsedText}{:else}{#if tc.toolName === 'request_review'}Writing Summary{:else if tc.toolName === 'ask_question'}Writing Question{:else if tc.toolName === 'bash' && isBashExecuting}Running{#if tc.path}: {tc.path}{/if}{:else if tc.toolName === 'read_file'}Preparing{#if tc.path}: {tc.path}{/if}{:else if tc.toolName === 'edit_file'}Editing{#if tc.path}: {tc.path}{/if}{:else}Writing{#if tc.path}: {tc.path}{/if}{#if tc.bytesWritten !== undefined}: {formatBytes(tc.bytesWritten)}{/if}{/if}{/if}</span></span>
            {:else if !tc.success}
                <span class="tool-status error">Failed</span>
            {:else}
                <span class="tool-status success">Completed</span>
            {/if}
        </div>
        {#if hasEditDiff}
            <button class="edit-diff-toggle" onclick={() => editDiffExpanded = !editDiffExpanded} aria-expanded={editDiffExpanded}>
                <span class="lines-added">+{editLinesAdded()}</span>
                <span class="lines-removed">/-{editLinesRemoved()}</span>
                <span class="edit-diff-arrow">{editDiffExpanded ? '▴' : '▾'}</span>
            </button>
            {#if editDiffExpanded}
                <div class="edit-diff" transition:slide={{ duration: 200 }}>
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
            {/if}
            {#if !tc.success && tc.result && tc.result.trim()}
                <pre class="tool-result tool-result-error">{tc.result}</pre>
            {/if}
        {:else if tc.toolName === 'write_file' && !tc.isPending}
            <div class="tool-result tool-write-result">
                <span class="write-result-text">Wrote {formatBytes(tc.bytesWritten ?? 0)} to:</span>
                <span class="write-result-path">{tc.path || tc.args?.path || '(unknown)'}</span>
            </div>
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
        {:else if tc.toolName === 'edit_task' && tc.result}
            {#if editResult && editResult.task_id && editResult.title}
                <div class="tool-result tool-edit-task">
                    <span class="edit-task-text">Edited task</span>
                    <span class="edit-task-title">{editResult.title as string}</span>
                </div>
            {:else}
                <pre class="tool-result" class:tool-result-error={!tc.success}>{tc.result}</pre>
            {/if}
        {:else if tc.toolName === 'view_tasks' && tc.result}
            {#if viewTasksResult !== null}
                <div class="tool-result tool-view-tasks">
                    <div class="view-tasks-header">{viewTasksResult.length} backlog task{viewTasksResult.length === 1 ? '' : 's'}</div>
                    {#each viewTasksResult as task}
                        <div class="view-task-item">
                            <span class="view-task-id">{task.id}</span>
                            <span class="view-task-title">{task.title}</span>
                            {#if task.parent_task_id}
                                <span class="view-task-parent">↳ {task.parent_task_id}</span>
                            {/if}
                        </div>
                    {/each}
                </div>
            {:else}
                <pre class="tool-result" class:tool-result-error={!tc.success}>{tc.result}</pre>
            {/if}
        {:else if tc.toolName === 'ask_question' && tc.args && tc.args.question}
            <div class="tool-result tool-result-question">{@html renderedQuestionHtml}</div>
        {:else if tc.toolName === 'request_review' && tc.args && tc.args.summary}
            <div class="tool-result tool-result-summary">{@html renderedSummaryHtml}</div>
        {:else if tc.result && tc.result.trim()}
            {#if resultNeedsTruncation}
                <div class="tool-result-wrapper">
                    <pre class="tool-result" class:tool-result-error={!tc.isPending && !tc.success} class:tool-result-expanded={resultExpanded} bind:this={scrollEl} onscroll={handleResultScroll}>{#if resultLoading}Loading...{:else}{resultExpanded ? effectiveResult : getTruncatedResult(effectiveResult)}{/if}</pre>
                    <button class="tool-result-expand" onclick={() => handleResultToggle()} aria-expanded={resultExpanded} disabled={resultLoading}>
                        {resultLoading ? 'Loading...' : (resultExpanded ? 'Collapse' : `Show all (${bashResultStats.lineCount} lines, ${bashResultStats.fullLength > 1024 ? (bashResultStats.fullLength / 1024).toFixed(1) + 'KB' : bashResultStats.fullLength + 'B'})`)}
                    </button>
                </div>
            {:else}
                <pre class="tool-result" class:tool-result-error={!tc.isPending && !tc.success} bind:this={scrollEl} onscroll={handleResultScroll}>{effectiveResult}</pre>
            {/if}
        {/if}
        {#if tc.duration !== undefined && tc.toolName !== 'request_review' && tc.toolName !== 'ask_question'}
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
    .tool-args-expanded {
        white-space: pre-wrap;
        overflow-wrap: break-word;
        overflow-x: visible;
        min-width: 0;
        user-select: text;
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
    .lines-added { color: var(--color-status-green); }
    .lines-removed { color: var(--color-status-red); }
    .tool-status.error { color: var(--color-status-red); }
    .tool-status.success { color: var(--color-status-green); }
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
    .tool-result-summary,
    .tool-result-question {
        font-family: inherit;
        font-size: 0.875rem;
        line-height: 1.875rem;
        color: var(--color-text-primary);
        word-break: break-word;
        white-space: normal;
        min-width: 0;
        overflow: hidden;
        max-height: none;
        overflow-y: visible;
        background: var(--color-surface-3);
        border-radius: 1.125rem;
        padding: 10px 14px;
    }
    .tool-result-question {
        margin: 4px 0 0;
    }
    .tool-result-summary :global(p),
    .tool-result-question :global(p) {
        margin: 0.75rem 0;
        line-height: 1.7;
    }
    .tool-result-summary :global(p:first-child),
    .tool-result-question :global(p:first-child) {
        margin-top: 0;
    }
    .tool-result-summary :global(p:last-child),
    .tool-result-question :global(p:last-child) {
        margin-bottom: 0;
    }
    .tool-result-summary :global(ul),
    .tool-result-summary :global(ol),
    .tool-result-question :global(ul),
    .tool-result-question :global(ol) {
        margin: 0.75rem 0;
        padding-left: 1.5rem;
        line-height: 1.7;
    }
    .tool-result-summary :global(ul:first-child),
    .tool-result-summary :global(ol:first-child),
    .tool-result-question :global(ul:first-child),
    .tool-result-question :global(ol:first-child) {
        margin-top: 0;
    }
    .tool-result-summary :global(ul:last-child),
    .tool-result-summary :global(ol:last-child),
    .tool-result-question :global(ul:last-child),
    .tool-result-question :global(ol:last-child) {
        margin-bottom: 0;
    }
    .tool-result-summary :global(li),
    .tool-result-question :global(li) {
        margin: 0.125rem 0;
        line-height: 1.7;
    }
    .tool-result-summary :global(li > p),
    .tool-result-question :global(li > p) {
        margin: 0.25rem 0;
        line-height: 1.7;
    }
    .tool-result-summary :global(pre),
    .tool-result-question :global(pre) {
        background: var(--color-surface-0);
        padding: 8px;
        border-radius: var(--radius-sm);
        overflow-x: auto;
    }
    .tool-result-summary :global(code),
    .tool-result-question :global(code) {
        font-family: var(--font-mono);
        font-size: 0.8125rem;
    }
    .tool-result-summary :global(:not(pre) > code),
    .tool-result-question :global(:not(pre) > code) {
        background-color: #151515;
        padding: 1px 4px;
        border-radius: 3px;
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
    .tool-write-result {
        display: flex;
        align-items: center;
        gap: 6px;
        background: var(--color-surface-0);
        padding: 6px 8px;
        border-radius: var(--radius-sm);
        margin: 4px 0 0;
        font-size: 0.75rem;
    }
    .write-result-text {
        color: var(--color-text-secondary);
    }
    .write-result-path {
        color: var(--color-text-primary);
        font-family: var(--font-mono);
        font-weight: 500;
    }
    .create-task-text {
        color: var(--color-text-secondary);
        font-size: 0.75rem;
    }
    .tool-edit-task {
        display: flex;
        align-items: center;
        gap: 6px;
        background: var(--color-surface-0);
        padding: 6px 8px;
        border-radius: var(--radius-sm);
        margin: 4px 0 0;
        font-size: 0.75rem;
    }
    .edit-task-text {
        color: var(--color-text-secondary);
    }
    .edit-task-title {
        color: var(--color-text-primary);
        font-weight: 500;
        font-family: var(--font-mono);
    }
    .tool-view-tasks {
        background: var(--color-surface-0);
        padding: 6px 8px;
        border-radius: var(--radius-sm);
        margin: 4px 0 0;
        font-size: 0.75rem;
    }
    .view-tasks-header {
        color: var(--color-text-secondary);
        margin-bottom: 4px;
        font-weight: 500;
    }
    .view-task-item {
        display: flex;
        align-items: baseline;
        gap: 6px;
        padding: 2px 0;
    }
    .view-task-id {
        color: var(--color-text-tertiary);
        font-family: var(--font-mono);
        font-size: 0.6875rem;
        flex-shrink: 0;
    }
    .view-task-title {
        color: var(--color-text-primary);
        font-weight: 500;
        flex: 1;
        min-width: 0;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .view-task-parent {
        color: var(--color-text-tertiary);
        font-family: var(--font-mono);
        font-size: 0.6875rem;
        flex-shrink: 0;
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
    }
    .btn-open-task-text {
        text-align: left;
        overflow-wrap: break-word;
        word-break: break-word;
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
    .edit-diff-toggle {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        background: var(--color-surface-0);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        padding: 4px 10px;
        margin: 4px 0 0;
        cursor: pointer;
        font-family: var(--font-mono);
        font-size: 0.75rem;
        font-weight: 600;
        color: var(--color-text-secondary);
        transition: all 0.15s ease;
    }
    .edit-diff-toggle:hover {
        background: var(--color-surface-2);
        border-color: var(--color-border-bright);
        color: var(--color-text-primary);
    }
    .edit-diff-arrow {
        font-size: 0.625rem;
        color: var(--color-text-tertiary);
        margin-left: 2px;
    }
</style>
