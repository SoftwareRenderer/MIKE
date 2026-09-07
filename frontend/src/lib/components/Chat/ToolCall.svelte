<script lang="ts">
    import { markdownToSafeHTML } from '$lib/utils/sanitize';
    import { getToolDisplayName } from '$lib/types';
    import { buildModifiedSegments } from '$lib/utils/diff-parser';
    import { slide } from 'svelte/transition';
    import Icon from '$lib/components/Icon.svelte';
    import shieldGreenIcon from '$lib/assets/icons/shield-green.svg?raw';
    import shieldOffIcon from '$lib/assets/icons/shield-off.svg?raw';
    import zapIcon from '$lib/assets/icons/zap.svg?raw';
    import fileTextIcon from '$lib/assets/icons/file-text.svg?raw';
    import searchIcon from '$lib/assets/icons/search.svg?raw';
    import editIcon from '$lib/assets/icons/edit.svg?raw';
    import checkCircleIcon from '$lib/assets/icons/check-circle.svg?raw';
    import questionIcon from '$lib/assets/icons/question.svg?raw';
    import { formatToolArgs, normalizeEdits, HIDE_ARGS_FOR_TOOLS, SANDBOXABLE_TOOLS, ARGS_MAX_LEN, EDIT_DIFF_MAX_LINES } from '$lib/utils/toolArgs';
    import type { EditDiffEntry } from '$lib/utils/toolArgs';
    import { formatElapsed, formatDuration } from '$lib/utils/format-time';
    import { formatBytes } from '$lib/utils/format';
    import { getTaskMessageFull } from '$lib/api';
    import { t } from '$lib/i18n';

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
        editDiffOverrides: Record<string, boolean>;
        projectId?: string;
        taskId?: string;
        onToggleArgs: (toolId: string) => void;
        onToggleEditDiff: (toolId: string, currentExpanded: boolean) => void;
        onNavigateTask?: (taskId: string) => void;
    }

    let { toolCall: tc, expandedArgs, onToggleArgs, editDiffOverrides, onToggleEditDiff, onNavigateTask, projectId, taskId }: Props = $props();

    // For bash: distinguish streaming (command being generated) from executing (command running).
    // Streaming: raw_args is partial JSON. Executing: raw_args is complete JSON (or args.command set).
    let isBashExecuting = $derived(
        tc.toolName === 'bash' && tc.isPending && (
            !!tc.args.command ||
            (tc.raw_args && (() => { try { JSON.parse(tc.raw_args); return true; } catch { return false; } })())
        )
    );

    // Elapsed time for pending tool calls — updates every second via timer.
    // Non-reactive timerRef: state would cause an effect read/write self-loop.
    let elapsedMs = $state(0);
    let timerRef: ReturnType<typeof setInterval> | null = null;

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
            ? t('chat.tool.runningFor', { elapsed: formatElapsed(elapsedMs) })
            : ''
    );

    // Pending-tool-call progress line. Every variant is one message: the `: {path}` / `: {bytes}`
    // clauses the markup used to bolt onto a bare word are part of the copy now, so a language
    // that puts them elsewhere can say so. `bash` before its command is complete deliberately
    // falls through to the generic "Writing" branch, exactly as the original condition chain did.
    let progressText = $derived.by(() => {
        if (elapsedText) return elapsedText;
        if (tc.toolName === 'request_review') return t('chat.tool.writingSummary');
        if (tc.toolName === 'ask_question') return t('chat.tool.writingQuestion');
        if (tc.toolName === 'edit_file') return t('chat.tool.editing');
        if (tc.toolName === 'write_file') return t('chat.tool.writingFile');
        if (tc.toolName === 'bash' && isBashExecuting) {
            return tc.path ? t('chat.tool.runningPath', { path: tc.path }) : t('chat.tool.running');
        }
        if (tc.toolName === 'read_file') {
            return tc.path ? t('chat.tool.preparingPath', { path: tc.path }) : t('chat.tool.preparing');
        }
        const bytes = tc.bytesWritten !== undefined ? formatBytes(tc.bytesWritten) : undefined;
        if (tc.path && bytes !== undefined) return t('chat.tool.writingPathBytes', { path: tc.path, bytes });
        if (tc.path) return t('chat.tool.writingPath', { path: tc.path });
        if (bytes !== undefined) return t('chat.tool.writingBytes', { bytes });
        return t('chat.tool.writing');
    });

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

    // LSP tools (hover, references, definition) use a magnifying glass icon
    const LSP_TOOLS = new Set(['lsp_hover', 'lsp_references', 'lsp_definition']);

    // Icon selection
    let iconInfo = $derived(
        tc.toolName === 'read_file' ? { src: fileTextIcon } :
        tc.toolName === 'write_file' || tc.toolName === 'edit_file' || tc.toolName === 'lsp_rename' ? { src: editIcon } :
        tc.toolName === 'request_review' ? { src: checkCircleIcon } :
        tc.toolName === 'ask_question' ? { src: questionIcon } :
        LSP_TOOLS.has(tc.toolName) ? { src: searchIcon } :
        { src: zapIcon }
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

    import type { InlineDiffSegment } from '$lib/types/diff';

    interface EditDiffLine { type: 'context' | 'removed' | 'added'; text: string; segments?: InlineDiffSegment[]; }
    interface EditDiffBlock { edits: EditDiffLine[]; separator?: boolean; }

    function formatEditDiff(edits: EditDiffEntry[]): EditDiffBlock[] {
        const blocks: EditDiffBlock[] = [];
        for (const edit of edits) {
            const lines: EditDiffLine[] = [];
            const oldLines = edit.oldText.split('\n');
            const newLines = edit.newText.split('\n');
            const maxLines = EDIT_DIFF_MAX_LINES;
            const oldSlice = oldLines.slice(0, maxLines);
            const newSlice = newLines.slice(0, maxLines);
            const pairCount = Math.min(oldSlice.length, newSlice.length);

            // Pair positionally
            for (let p = 0; p < pairCount; p++) {
                const oldLine = oldSlice[p]!;
                const newLine = newSlice[p]!;
                if (oldLine === newLine) {
                    // Identical — show as single context line
                    lines.push({ type: 'context', text: oldLine });
                } else {
                    // Different — show removed+added with inline segments
                    const { oldSegments, newSegments } = buildModifiedSegments(oldLine, newLine);
                    lines.push({ type: 'removed', text: oldLine, segments: oldSegments });
                    lines.push({ type: 'added', text: newLine, segments: newSegments });
                }
            }

            // Unpaired removed lines (more old lines than new)
            for (let p = pairCount; p < oldSlice.length; p++) {
                lines.push({ type: 'removed', text: oldSlice[p] ?? '' });
            }

            // Unpaired added lines (more new lines than old)
            for (let p = pairCount; p < newSlice.length; p++) {
                lines.push({ type: 'added', text: newSlice[p] ?? '' });
            }

            blocks.push({ edits: lines, separator: true });
        }
        return blocks;
    }

    /** Count actual added/removed lines by comparing old/new text positionally. */
    function countEditChanges(edits: EditDiffEntry[]): { added: number; removed: number } {
        let added = 0;
        let removed = 0;
        for (const edit of edits) {
            const oldLines = edit.oldText.split('\n');
            const newLines = edit.newText.split('\n');
            const pairCount = Math.min(oldLines.length, newLines.length);
            for (let p = 0; p < pairCount; p++) {
                if (oldLines[p] !== newLines[p]) { added++; removed++; }
            }
            removed += Math.max(0, oldLines.length - pairCount);
            added += Math.max(0, newLines.length - pairCount);
        }
        return { added, removed };
    }

    // Model/agent args are not trustworthy: an edit may be missing a side
    // (still streaming, truncated, or hallucinated keys). Normalize once so
    // every consumer (count + diff) reads string-guaranteed entries and no
    // renderer can hit `undefined.split()`.
    let editEntries = $derived<EditDiffEntry[]>(normalizeEdits(tc.args?.edits));

    // Edit diff expand/collapse — auto-expand for tiny changes (≤2 lines).
    // State is managed by parent (ChatMessage) to survive virtual list remounts.
    let _editChanges = $derived(countEditChanges(editEntries));
    let isEditDiffTrivial = $derived((_editChanges.added + _editChanges.removed) <= 2);
    // Use explicit user override if set, otherwise fall back to trivial auto-expand default
    let editDiffExpanded = $derived(
        tc.toolId in editDiffOverrides ? editDiffOverrides[tc.toolId]! : isEditDiffTrivial
    );
    let editLinesAdded = $derived(() => _editChanges.added);
    let editLinesRemoved = $derived(() => _editChanges.removed);
    let hasEditDiff = $derived(tc.toolName === 'edit_file' && editEntries.length > 0);

    // Rendered markdown for request_review summary
    let renderedSummaryHtml = $derived(
        tc.toolName === 'request_review' && tc.args && tc.args.summary
            ? markdownToSafeHTML(tc.args.summary as string)
            : ''
    );

    // Rendered markdown for ask_question
    let renderedQuestionHtml = $derived(
        tc.toolName === 'ask_question' && tc.args && tc.args.question
            ? markdownToSafeHTML(tc.args.question as string)
            : ''
    );

    // Auto-answered when the tool result differs from the question (a human-
    // answered question keeps its result as the question).
    let isAutoAnswered = $derived(
        tc.toolName === 'ask_question' && !!tc.result && tc.result !== (tc.args && tc.args.question)
    );
    let renderedAutoAnswerHtml = $derived(
        isAutoAnswered && tc.result
            ? markdownToSafeHTML(tc.result as string)
            : ''
    );

    // Bash timeout (in seconds, from args or raw_args fallback)
    // Computed inline to avoid Svelte 5 $derived function-call TypeScript issues.
    let bashTimeout = $derived<number | undefined>(
        tc.toolName !== 'bash' ? undefined :
        tc.args && 'timeout' in tc.args ? (tc.args.timeout as number) || undefined :
        tc.raw_args ? (() => { const m = tc.raw_args!.match(/"timeout"\s*:\s*(\d+)/); return m ? parseInt(m[1], 10) : undefined; })() :
        undefined
    );
    let timeoutBadge = $derived(bashTimeout !== undefined ? t('chat.tool.timeout', { elapsed: formatElapsed(bashTimeout * 1000) }) : '');
</script>

<div class="tool-call-entry">
    <div class="tool-call-card" style="border-left-color: {tc.isPending ? 'var(--color-accent)' : (tc.success ? 'var(--color-status-green)' : 'var(--color-status-red)')}">
        <div class="tool-call-header">
            {#if tc.sandbox}
                <Icon class="tool-sandbox-icon" src={shieldGreenIcon} color="var(--color-status-green)" width={14} height={14} title={t('chat.tool.sandboxed')} />
            {:else if SANDBOXABLE_TOOLS.has(tc.toolName)}
                <Icon class="tool-sandbox-icon" src={shieldOffIcon} color="var(--color-status-red)" width={14} height={14} title={t('chat.tool.notSandboxed')} />
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
                <Icon class="tool-icon" src={iconInfo.src} width={14} height={14} />
            {/if}
            <span class="tool-name" title={tc.toolName}>{getToolDisplayName(tc.displayName, tc.toolName)}</span>
            {#if showArgs}
                {#if expandedArgs.has(tc.toolId)}
                    <button class="tool-args-link" onclick={handleArgsClick} onmousedown={handleArgsMouseDown} onmouseup={handleArgsMouseUp} aria-label={t('chat.tool.collapseArgs')}>
                        <pre class="tool-args tool-args-expanded">{argsDisplay}</pre>
                    </button>
                {:else}
                    {#if truncated.truncated}
                        <button class="tool-args-link" onclick={() => onToggleArgs(tc.toolId)} aria-label={t('chat.tool.expandArgs')}>
                            <pre class="tool-args tool-args-collapsed">{truncated.text}</pre>
                        </button>
                    {:else}
                        <pre class="tool-args">{argsDisplay}</pre>
                    {/if}
                {/if}
            {/if}
            {#if tc.isPending}
                <span class="tool-status pending"><span class="spinner"></span><span class="progress-text">{progressText}</span></span>
            {:else if !tc.success}
                <span class="tool-status error">{t('chat.tool.failed')}</span>
            {:else}
                <span class="tool-status success">{t('chat.tool.completed')}</span>
            {/if}
        </div>
        {#if timeoutBadge && tc.isPending}
            <div class="tool-timeout-badge">{timeoutBadge}</div>
        {/if}
        {#if hasEditDiff}
            <button class="edit-diff-toggle" onclick={() => onToggleEditDiff(tc.toolId, editDiffExpanded)} aria-expanded={editDiffExpanded}>
                <span class="lines-added">+{editLinesAdded()}</span>
                <span class="lines-removed">/-{editLinesRemoved()}</span>
                <span class="edit-diff-arrow">{editDiffExpanded ? '▴' : '▾'}</span>
            </button>
            {#if editDiffExpanded}
                <div class="edit-diff" transition:slide={{ duration: 200 }}>
                    {#each formatEditDiff(editEntries) as block}
                        {#each block.edits as line}
                            <div class="edit-diff-line edit-{line.type}">
                                <span class="edit-diff-sign">{line.type === 'removed' ? '-' : line.type === 'added' ? '+' : ' '}</span>
                                {#if line.segments}
                                    <span class="edit-diff-text">
                                        {#each line.segments as seg}
                                            <span class="edit-segment edit-segment-{seg.tone}">{seg.text}</span>
                                        {/each}
                                    </span>
                                {:else}
                                    <span class="edit-diff-text">{line.text}</span>
                                {/if}
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
            {#if tc.duration !== undefined}
                <span class="tool-duration">
                    {formatDuration(tc.duration)}
                </span>
            {/if}
        {:else if tc.toolName === 'write_file' && !tc.isPending}
            <div class="tool-result tool-write-result">
                <span class="write-result-text">{t('chat.tool.wroteTo', { bytes: formatBytes(tc.bytesWritten ?? 0) })}</span>
                <span class="write-result-path">{tc.path || tc.args?.path || t('chat.tool.unknownPath')}</span>
            </div>
        {:else if tc.toolName === 'create_task' && tc.result && tc.success}
            {#if createResult && createResult.task_id}
                <div class="tool-result tool-create-task">
                    <span class="create-task-text">{t('chat.tool.createdTask')}</span>
                    <button class="btn-open-task" onclick={() => onNavigateTask?.(createResult.task_id as string)} title={t('chat.tool.openTask')}>
                        <span class="btn-open-task-text">{createResult.title as string}</span>
                    </button>
                </div>
            {:else}
                <pre class="tool-result" class:tool-result-error={!tc.success}>{tc.result}</pre>
            {/if}
        {:else if tc.toolName === 'edit_task' && tc.result}
            {#if editResult && editResult.task_id && editResult.title}
                <div class="tool-result tool-edit-task">
                    <span class="edit-task-text">{t('chat.tool.editedTask')}</span>
                    <span class="edit-task-title">{editResult.title as string}</span>
                </div>
            {:else}
                <pre class="tool-result" class:tool-result-error={!tc.success}>{tc.result}</pre>
            {/if}
        {:else if tc.toolName === 'view_tasks' && tc.result}
            {#if viewTasksResult !== null}
                <div class="tool-result tool-view-tasks">
                    <div class="view-tasks-header">{t('chat.tool.backlogTasks', { count: viewTasksResult.length })}</div>
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
            {#if isAutoAnswered}
                <div class="tool-result tool-result-auto-answer">
                    <div class="auto-answer-label">{t('chat.tool.autoAnswer')}</div>
                    <div class="auto-answer-body">{@html renderedAutoAnswerHtml}</div>
                </div>
            {/if}
        {:else if tc.toolName === 'request_review' && tc.args && tc.args.summary}
            <div class="tool-result tool-result-summary">{@html renderedSummaryHtml}</div>
        {:else if tc.result && tc.result.trim()}
            {#if resultNeedsTruncation}
                <div class="tool-result-wrapper">
                    <pre class="tool-result" class:tool-result-error={!tc.isPending && !tc.success} class:tool-result-expanded={resultExpanded} bind:this={scrollEl} onscroll={handleResultScroll}>{#if resultLoading}{t('chat.common.loading')}{:else}{resultExpanded ? effectiveResult : getTruncatedResult(effectiveResult)}{/if}</pre>
                    <button class="tool-result-expand" onclick={() => handleResultToggle()} aria-expanded={resultExpanded} disabled={resultLoading}>
                        {resultLoading ? t('chat.common.loading') : (resultExpanded ? t('chat.tool.collapse') : t('chat.tool.showAll', { lines: bashResultStats.lineCount, size: bashResultStats.fullLength > 1024 ? (bashResultStats.fullLength / 1024).toFixed(1) + 'KB' : bashResultStats.fullLength + 'B' }))}
                    </button>
                </div>
            {:else}
                <pre class="tool-result" class:tool-result-error={!tc.isPending && !tc.success} bind:this={scrollEl} onscroll={handleResultScroll}>{effectiveResult}</pre>
            {/if}
        {/if}
        {#if tc.duration !== undefined && tc.toolName !== 'request_review' && tc.toolName !== 'ask_question' && tc.toolName !== 'edit_file'}
            <span class="tool-duration">
                {formatDuration(tc.duration)}
                {#if bashTimeout !== undefined}
                    <span class="tool-duration-sep">·</span>
                    <span class="tool-timeout">{t('chat.tool.timeout', { elapsed: formatElapsed(bashTimeout * 1000) })}</span>
                {/if}
            </span>
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
    .tool-timeout-badge {
        font-size: 0.6875rem;
        color: var(--color-text-tertiary);
        padding: 2px 0;
    }
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
    .tool-result-auto-answer {
        margin: 6px 0 0;
        padding: 8px 12px;
        border-left: 3px solid var(--color-accent);
        background: var(--color-surface-1);
        border-radius: var(--radius-sm);
        font-size: 0.875rem;
        line-height: 1.7;
        color: var(--color-text-primary);
        word-break: break-word;
        white-space: normal;
        min-width: 0;
        overflow: hidden;
    }
    .auto-answer-label {
        font-size: 0.75rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.04em;
        color: var(--color-accent);
        margin-bottom: 4px;
    }
    .auto-answer-body :global(p) {
        margin: 0.5rem 0;
    }
    .auto-answer-body :global(p:first-child) {
        margin-top: 0;
    }
    .auto-answer-body :global(p:last-child) {
        margin-bottom: 0;
    }
    .auto-answer-body :global(code) {
        font-family: var(--font-mono);
        font-size: 0.8125rem;
        overflow-wrap: break-word;
        word-break: break-word;
    }
    .auto-answer-body :global(pre) {
        background: var(--color-surface-0);
        padding: 8px;
        border-radius: var(--radius-sm);
        overflow-x: auto;
        white-space: pre-wrap;
        word-break: break-word;
        overflow-wrap: break-word;
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
        white-space: pre-wrap;
        word-break: break-word;
        overflow-wrap: break-word;
    }
    .tool-result-summary :global(code),
    .tool-result-question :global(code) {
        font-family: var(--font-mono);
        font-size: 0.8125rem;
        overflow-wrap: break-word;
        word-break: break-word;
    }
    .tool-result-summary :global(:not(pre) > code),
    .tool-result-question :global(:not(pre) > code) {
        background-color: var(--color-surface-1);
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
        display: inline-flex;
        align-items: center;
        gap: 4px;
        font-size: 0.6875rem;
        color: var(--color-text-tertiary);
        margin-top: 4px;
    }
    .tool-duration-sep {
        color: var(--color-text-tertiary);
        opacity: 0.6;
    }
    .tool-timeout {
        font-size: 0.6875rem;
        color: var(--color-text-tertiary);
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
    .edit-context {
        color: var(--color-text-tertiary);
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
    /* Inline word-level diff segments */
    .edit-segment {
        font-family: inherit;
        font-size: inherit;
        white-space: inherit;
    }
    .edit-segment-added {
        background: rgba(63,185,80,0.4);
        border-radius: 2px;
    }
    .edit-segment-removed {
        background: rgba(248,81,73,0.4);
        border-radius: 2px;
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
