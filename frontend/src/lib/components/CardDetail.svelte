<script lang="ts">
    import { onMount } from 'svelte';
    import { get } from 'svelte/store';
    import type { Task } from '$lib/types';
    import type { DiffFile } from '$lib/types/diff';
    import { board as boardStore, selectedTask, streamingTaskIds, showTerminal } from '$lib/store';
    import { moveTask, deleteTask, getTaskDiff, getBoard, updateTask, checkWorktreeExists } from '$lib/api';
    import { parseUnifiedDiff } from '$lib/utils/diff-parser';
import { getWorktreeDisplayPath } from '$lib/utils/path';
    import TaskChat from './Chat/TaskChat.svelte';
    import ResizeHandle from './ResizeHandle.svelte';
    import DiffViewer from './DiffViewer.svelte';

    let { task, tasks: columnTasks, projectPath, workspaceDir, onClose }: { task: Task; tasks: Task[]; projectPath?: string; workspaceDir?: string; onClose: () => void } = $props();

    let allTasks: Task[] = $state([]);

    const overviewSections = [
        { id: 'backlog', title: 'Backlog' },
        { id: 'in_progress', title: 'In Progress' },
        { id: 'review', title: 'Review' },
    ];

    // Collapsible sections
    let collapsedSections: Set<string> = $state(new Set());

    function toggleSection(sectionId: string) {
        const next = new Set(collapsedSections);
        if (next.has(sectionId)) {
            next.delete(sectionId);
        } else {
            next.add(sectionId);
        }
        collapsedSections = next;
    }

    function getSectionColor(id: string): string {
        const colors: Record<string, string> = {
            backlog: 'var(--color-column-backlog)',
            in_progress: 'var(--color-column-in-progress)',
            review: 'var(--color-column-review)',
        };
        return colors[id] || 'var(--color-border)';
    }

    // Diff state
    let diffFiles: DiffFile[] = $state([]);
    let untrackedFiles: { path: string; fileSize?: number; file: DiffFile }[] = $state([]);
    // Terminal
    let worktreeExists = $state(true);

    async function checkWorktree() {
        if (!task.project_id) return;
        try {
            const result = await checkWorktreeExists(task.project_id, task.id);
            worktreeExists = result.exists;
        } catch {
            worktreeExists = false;
        }
    }

    // Re-check worktree when the board is refreshed (task status may have changed)
    // Uses $boardStore (store $-prefix) which is guaranteed reactive in Svelte 5
    $effect(() => {
        const currentTaskId = $boardStore?.tasks?.find(t => t.id === task.id)?.id;
        if (currentTaskId) {
            checkWorktree();
        }
    });

    // Inline title editing for task list items
    let editingTaskId = $state<string | null>(null);
    let editingTitleDraft = $state('');
    let editingTitleInput: HTMLInputElement | null = $state(null);

    function startEditTaskTitle(taskId: string, currentTitle: string, e: MouseEvent) {
        e.stopPropagation();
        editingTaskId = taskId;
        editingTitleDraft = currentTitle;
    }

    function finishEditTaskTitle() {
        if (editingTaskId && editingTitleDraft.trim() && task.project_id) {
            const trimmed = editingTitleDraft.trim();
            const targetTask = allTasks.find(t => t.id === editingTaskId);
            if (targetTask && targetTask.title !== trimmed) {
                targetTask.title = trimmed;
                updateTask(task.project_id, editingTaskId, { title: trimmed }).catch(err => {
                    targetTask.title = editingTitleDraft;
                    console.error('Failed to update title:', err);
                });
            }
        }
        editingTaskId = null;
        editingTitleDraft = '';
    }

    function cancelEditTaskTitle() {
        editingTaskId = null;
        editingTitleDraft = '';
    }

    $effect(() => {
        if (editingTaskId && editingTitleInput) {
            editingTitleInput.focus();
            editingTitleInput.select();
        }
    });

    // Status badge
    let badge = $derived.by(() => {
        const streaming = $streamingTaskIds.has(task.id);
        const state = $boardStore?.tasks.find(t => t.id === task.id)?.status_state ?? task.status_state;
        if (streaming || state === 'running') return { text: 'Running', color: 'var(--color-status-blue)' };
        if (state === 'failed') return { text: 'Failed', color: 'var(--color-status-red)' };
        if (state === 'stopped') return { text: 'Stopped', color: 'var(--color-status-yellow)' };
        return { text: 'Idle', color: 'var(--color-text-tertiary)' };
    });

    // Mobile tab
    let activeTab = $state<'context' | 'chat' | 'diff'>('chat');
    let isMobile = $derived(false);
    let touchStartX = $state(0);
    let touchEndX = $state(0);

    // Column resize
    let columnsRef: HTMLDivElement | null = $state(null);
    const BOARD_RATIO_KEY = 'kanban:detail:boardRatio';
    const CHAT_RATIO_KEY = 'kanban:detail:chatRatio';
    const DEFAULT_BOARD_RATIO = 0.22;  // ~220px out of ~1000px
    const DEFAULT_CHAT_RATIO = 0.55;   // 55% of remaining space
    const MIN_BOARD_RATIO = 0.12;
    const MAX_BOARD_RATIO = 0.40;
    const MIN_CHAT_RATIO = 0.15;
    const MAX_CHAT_RATIO = 0.80;

    let boardRatio = $state(loadRatio(BOARD_RATIO_KEY, DEFAULT_BOARD_RATIO));
    let chatRatio = $state(loadRatio(CHAT_RATIO_KEY, DEFAULT_CHAT_RATIO));

    function loadRatio(key: string, defaultVal: number): number {
        try {
            const stored = localStorage.getItem(key);
            if (stored !== null) {
                const val = Number.parseFloat(stored);
                if (!isNaN(val) && val > 0) return val;
            }
        } catch {}
        return defaultVal;
    }

    function saveRatio(key: string, value: number) {
        try {
            localStorage.setItem(key, String(value));
        } catch {}
    }

    function clampBoardRatio(val: number): number {
        return Math.max(MIN_BOARD_RATIO, Math.min(MAX_BOARD_RATIO, val));
    }
    function clampChatRatio(val: number): number {
        return Math.max(MIN_CHAT_RATIO, Math.min(MAX_CHAT_RATIO, val));
    }

    // Computed column widths as percentages
    let boardWidth = $derived(`${(boardRatio * 100).toFixed(1)}%`);
    let remainingWidth = $derived(`${((1 - boardRatio) * 100).toFixed(1)}%`);
    let chatWidth = $derived(`${(chatRatio * (1 - boardRatio) * 100).toFixed(1)}%`);
    let diffWidth = $derived(`${(((1 - chatRatio) * (1 - boardRatio)) * 100).toFixed(1)}%`);

    // Drag handlers for resize handles
    let boardDragStartX = $state(0);
    let boardDragStartRatio = $state(0);
    let chatDragStartX = $state(0);
    let chatDragStartRatio = $state(0);

    function onBoardDragStart(_event: MouseEvent, startX: number) {
        boardDragStartX = startX;
        boardDragStartRatio = boardRatio;
    }
    function onBoardDragMove(pointerX: number) {
        const container = columnsRef;
        if (!container) return;
        const containerWidth = Math.max(container.offsetWidth, 1);
        const delta = (pointerX - boardDragStartX) / containerWidth;
        boardRatio = clampBoardRatio(boardDragStartRatio + delta);
    }
    function onBoardDragEnd() {
        saveRatio(BOARD_RATIO_KEY, boardRatio);
    }

    function onChatDragStart(_event: MouseEvent, startX: number) {
        chatDragStartX = startX;
        chatDragStartRatio = chatRatio;
    }
    function onChatDragMove(pointerX: number) {
        const container = columnsRef;
        if (!container) return;
        // The chat column occupies (1 - boardRatio) of the container
        const remainingWidth = Math.max(container.offsetWidth * (1 - boardRatio), 1);
        const delta = (pointerX - chatDragStartX) / remainingWidth;
        chatRatio = clampChatRatio(chatDragStartRatio + delta);
    }
    function onChatDragEnd() {
        saveRatio(CHAT_RATIO_KEY, chatRatio);
    }

    $effect(() => {
        const check = () => isMobile = window.matchMedia('(max-width: 768px)').matches;
        check();
        window.addEventListener('resize', check);
        return () => window.removeEventListener('resize', check);
    });

    function handleTouchStart(e: TouchEvent) {
        touchStartX = e.touches[0].clientX;
    }

    function handleTouchEnd(e: TouchEvent) {
        touchEndX = e.changedTouches[0].clientX;
        processSwipe();
    }

    function handlePointerDown(e: PointerEvent) {
        touchStartX = e.clientX;
    }

    function handlePointerUp(e: PointerEvent) {
        touchEndX = e.clientX;
        processSwipe();
    }

    function processSwipe() {
        const diff = touchStartX - touchEndX;
        const threshold = 50;
        if (Math.abs(diff) > threshold) {
            if (diff > 0 && activeTab !== 'diff') {
                // Swiped left — go next
                if (activeTab === 'context') activeTab = 'chat';
                else if (activeTab === 'chat') activeTab = 'diff';
            } else if (diff < 0 && activeTab !== 'context') {
                // Swiped right — go prev
                if (activeTab === 'diff') activeTab = 'chat';
                else if (activeTab === 'chat') activeTab = 'context';
            }
        }
    }

    onMount(() => {
        const unsubBoard = boardStore.subscribe(b => {
            if (b) {
                allTasks = b.tasks;
            } else {
                allTasks = [];
            }
        });

        checkWorktree();
        loadDiff();

        function handleKey(e: KeyboardEvent) {
            if (e.key === 'Escape') onClose();
        }
        window.addEventListener('keydown', handleKey);
        return () => {
            unsubBoard();
            window.removeEventListener('keydown', handleKey);
        };
    });

    async function handleMove(status: string) {
        if (!task) return;
        try {
            const updated = await moveTask(task.project_id, task.id, status);
            task = updated;
            boardStore.update(b => {
                if (!b) return b;
                const idx = b.tasks.findIndex(t => t.id === task!.id);
                if (idx !== -1) b.tasks[idx] = updated;
                return { ...b };
            });
        } catch (e) {
            alert('Failed to move: ' + String(e));
        }
    }

    async function handleDelete() {
        if (!task || !confirm('Delete this task? This cannot be undone.')) return;
        const projectId = task.project_id;
        const taskId = task.id;
        try {
            await deleteTask(projectId, taskId);
            const board = await getBoard(projectId);
            boardStore.set(board);
            onClose();
        } catch (e) {
            alert('Failed to delete task: ' + String(e));
        }
    }

    async function loadDiff() {
        if (!task) return;
        try {
            const result = await getTaskDiff(task.project_id, task.id);
            const ignoreSwp = (p: string) => !p.split('/').pop()?.match(/\..*\.swp$/);
            diffFiles = parseUnifiedDiff(result.diff).filter(f => ignoreSwp(f.path));
            untrackedFiles = (result.untracked || []).filter(u => ignoreSwp(u.path)).map(u => ({
                path: u.path,
                fileSize: u.file_size,
                file: parseUnifiedDiff(u.diff)[0] || { path: u.path, added: 0, removed: 0, rows: [] },
            }));
        } catch (e) {
            diffFiles = [];
            untrackedFiles = [];
        }
    }

    function getStatusBadge(t: Task): { text: string; color: string } {
        const streaming = get(streamingTaskIds).has(t.id);
        const state = $boardStore?.tasks.find(tt => tt.id === t.id)?.status_state ?? t.status_state;
        if (streaming || state === 'running') return { text: 'Running', color: 'var(--color-status-blue)' };
        if (state === 'failed') return { text: 'Failed', color: 'var(--color-status-red)' };
        if (state === 'stopped') return { text: 'Stopped', color: 'var(--color-status-yellow)' };
        return { text: '', color: 'var(--color-text-tertiary)' };
    }


</script>

    <div class="task-detail-page">
        <!-- Header -->
        <div class="header">
            <div class="header-left">
                <button class="btn-back" onclick={onClose} aria-label="Back to board (Esc)">
                    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                        <path d="M10 3l-5 5 5 5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                    </svg>
                </button>
                <div class="header-main">
                    <span class="project-prefix" title={getWorktreeDisplayPath(workspaceDir || '', task.id)}>{getWorktreeDisplayPath(workspaceDir || '', task.id)}</span>
                    <span class="badge" style="background:{badge.color}18;color:{badge.color};border-color:{badge.color}40">
                        {badge.text}
                    </span>
                </div>
            </div>
            <div class="header-actions">
                {#if task.status === 'review'}
                    <button class="btn-icon btn-success-icon" onclick={() => handleMove('done')} aria-label="Approve and move to Done">
                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M4 8l3 3 5-6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                    </button>
                    <button class="btn-icon btn-secondary-icon" onclick={() => handleMove('in_progress')} aria-label="Reject and return to In Progress">
                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M12 4L4 12" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>
                    </button>
                {/if}
                <button class="btn-icon" onclick={() => showTerminal.set(true)} disabled={!worktreeExists} aria-label="Open terminal">
                    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                        <rect x="1" y="2" width="14" height="12" rx="1.5" stroke="currentColor" stroke-width="1.3"/>
                        <path d="M4 6l2 2-2 2M7 10h3" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                    </svg>
                </button>
                <button class="btn-icon" onclick={handleDelete} aria-label="Delete task">
                    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                        <path d="M2 4h12M5 4V3a1 1 0 011-1h4a1 1 0 011 1v1M6 7v5M10 7v5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                        <path d="M3 4l1 9a1 1 0 001 1h6a1 1 0 001-1l1-9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                    </svg>
                </button>
            </div>
        </div>

        <!-- Mobile Tab Bar -->
        {#if isMobile}
            <div class="mobile-tab-bar">
                <button
                    class="tab-btn {activeTab === 'context' ? 'active' : ''}"
                    onclick={() => activeTab = 'context'}
                >
                    <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M2 2h5v5H2zM9 2h5v5H9zM2 9h5v5H2zM9 9h5v5H9z" stroke="currentColor" stroke-width="1.2"/></svg>
                    Board
                </button>
                <button
                    class="tab-btn {activeTab === 'chat' ? 'active' : ''}"
                    onclick={() => activeTab = 'chat'}
                >
                    <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M2 3h12v8H5l-3 3V3z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round"/></svg>
                    Agent
                </button>
                <button
                    class="tab-btn {activeTab === 'diff' ? 'active' : ''}"
                    onclick={() => activeTab = 'diff'}
                >
                    <svg width="12" height="12" viewBox="0 0 16 16" fill="none"><path d="M4 2v12M12 2v12M4 6h3l-1 2 1 2H4M12 6H9l1 2-1 2h3" stroke="currentColor" stroke-width="1.2" stroke-linecap="round" stroke-linejoin="round"/></svg>
                    Changes
                </button>
            </div>
        {/if}

        <!-- 3-Column Layout -->
        <div bind:this={columnsRef} class="columns" class:mobile-swipe={isMobile} ontouchstart={handleTouchStart} ontouchend={handleTouchEnd} onpointerdown={handlePointerDown} onpointerup={handlePointerUp} role="region" aria-label="Task detail panels">
            <!-- Column 1: Board Overview -->
            <div class="column column-context" class:hidden={isMobile && activeTab !== 'context'} style:width={boardWidth}>
                <div class="task-list">
                    {#each overviewSections as section}
                        {@const sectionTasks = allTasks.filter(t => t.status === section.id).sort((a, b) => a.column_order - b.column_order)}
                        {#if sectionTasks.length > 0}
                            <div class="section-group" style="--col-color: {getSectionColor(section.id)}">
                                <button class="section-header" onclick={() => toggleSection(section.id)} type="button" aria-expanded={!collapsedSections.has(section.id)}>
                                    <span class="section-title">
                                        {#if section.id === 'backlog'}
                                            <svg class="col-icon" width="12" height="12" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" stroke="currentColor" stroke-width="1.5"/></svg>
                                        {:else if section.id === 'in_progress'}
                                            <svg class="col-icon" width="12" height="12" viewBox="0 0 10 10" fill="none" aria-hidden="true"><polygon points="3.5,1.5 3.5,8.5 8.5,5" fill="currentColor"/></svg>
                                        {:else if section.id === 'review'}
                                            <svg class="col-icon" width="12" height="12" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" stroke="currentColor" stroke-width="1"/><circle cx="5" cy="5" r="1.2" fill="currentColor"/></svg>
                                        {/if}
                                        {section.title}
                                    </span>
                                    <span class="section-count">{sectionTasks.length}</span>
                                    <svg class="section-chevron" width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                                </button>
                                {#if !collapsedSections.has(section.id)}
                                <div class="section-body">
                                    {#each sectionTasks as t (t.id)}
                                    {#if editingTaskId === t.id}
                                        <div class="task-item task-item-editing">
                                            <input
                                                class="task-item-title-input"
                                                type="text"
                                                bind:value={editingTitleDraft}
                                                onblur={finishEditTaskTitle}
                                                onkeydown={(e) => { if (e.key === 'Enter') finishEditTaskTitle(); if (e.key === 'Escape') cancelEditTaskTitle(); e.stopPropagation(); }}
                                                bind:this={editingTitleInput}
                                                placeholder="Task title"
                                            />
                                        </div>
                                    {:else}
                                        <button
                                            type="button"
                                            class={`task-item${t.id === task.id ? ' selected' : ''}`}
                                            onclick={() => { window.history.replaceState(null, '', '?task=' + t.id); selectedTask.set(t); activeTab = 'chat'; }}
                                            aria-label={`Select task: ${t.title || '(untitled)'}`}
                                            aria-current={t.id === task.id ? 'task' : undefined}
                                        >
                                            <span class="task-item-title">{t.title || '(untitled)'}</span>
                                            <span class="task-item-edit-btn" onclick={(e) => startEditTaskTitle(t.id, t.title, e)} role="button" aria-label="Edit title" title="Edit title" tabindex="0">
                                                <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M11.5 2.5l2 2L5 13H3v-2L11.5 2.5z" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round"/></svg>
                                            </span>
                                            {#if getStatusBadge(t).text}
                                                <span class="task-item-badge" style="color:{getStatusBadge(t).color}">
                                                    {getStatusBadge(t).text}
                                                </span>
                                            {/if}
                                        </button>
                                    {/if}
                                    {/each}
                                </div>
                                {/if}
                            </div>
                        {/if}
                    {/each}
                </div>
            </div>

            <!-- Resize handle: Board ↔ Chat -->
            {#if !isMobile}
                <ResizeHandle
                    ariaLabel="Resize board and agent panels"
                    onDragStart={onBoardDragStart}
                    onDragMove={onBoardDragMove}
                    onDragEnd={onBoardDragEnd}
                />
            {/if}

            <!-- Column 2: Chat / Agent Interface -->
            <div class="column column-chat" class:hidden={isMobile && activeTab !== 'chat'} style:width={chatWidth}>
                <div style="flex:1;min-height:0">
                    <TaskChat
                        projectId={task.project_id}
                        taskId={task.id}
                        taskPrompt={task.prompt}
                        disabled={task.status_state === 'completed'}
                        onRefreshDiff={loadDiff}
                    />
                </div>
            </div>

            <!-- Resize handle: Chat ↔ Diff -->
            {#if !isMobile}
                <ResizeHandle
                    ariaLabel="Resize agent and changes panels"
                    onDragStart={onChatDragStart}
                    onDragMove={onChatDragMove}
                    onDragEnd={onChatDragEnd}
                />
            {/if}

            <!-- Column 3: Working Changes (Diff) -->
            <div class="column column-diff" class:hidden={isMobile && activeTab !== 'diff'} style:width={diffWidth}>
                <div class="diff-content">
                    <DiffViewer files={diffFiles} untracked={untrackedFiles} />
                </div>
            </div>
        </div>
    </div>

<style>
    .task-detail-page {
        flex: 1;
        display: flex;
        flex-direction: column;
        overflow: hidden;
        height: 100%;
    }
    .header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 8px 16px;
        border-bottom: 1px solid var(--color-border);
        flex-shrink: 0;
        gap: 12px;
        background: var(--color-surface-1);
    }
    .header-left {
        display: flex;
        align-items: center;
        gap: 10px;
        min-width: 0;
        flex: 1;
    }
    .btn-back {
        background: none;
        border: none;
        color: var(--color-text-secondary);
        cursor: pointer;
        padding: 6px;
        border-radius: var(--radius-sm);
        display: flex;
        align-items: center;
        justify-content: center;
        flex-shrink: 0;
    }
    .btn-back:hover { background: var(--color-surface-3); color: var(--color-text-primary); }
    .header-main {
        display: flex;
        align-items: center;
        gap: 10px;
        min-width: 0;
        flex: 1;
    }
    .project-prefix {
        font-size: 0.875rem;
        font-weight: 400;
        color: var(--color-text-tertiary);
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
        flex: 1 1 0;
        min-width: 0;
    }
    .badge {
        padding: 2px 8px;
        border-radius: 10px;
        font-size: 0.75rem;
        font-weight: 600;
        flex-shrink: 0;
        border: 1px solid;
        text-transform: uppercase;
        letter-spacing: 0.3px;
    }
    .header-actions {
        display: flex;
        align-items: center;
        gap: 2px;
    }
    .btn-icon,
    .btn-icon-sm {
        background: none;
        border: none;
        color: var(--color-text-secondary);
        cursor: pointer;
        border-radius: var(--radius-sm);
        display: flex;
        align-items: center;
        justify-content: center;
    }
    .btn-icon { padding: 6px; }
    .btn-icon-sm { padding: 2px; }
    .btn-icon:hover { background: var(--color-surface-3); color: var(--color-text-primary); }
    .btn-icon:disabled { opacity: 0.35; cursor: not-allowed; }
    .btn-icon:disabled:hover { background: none; color: var(--color-text-secondary); }
    .btn-success-icon:hover { background: var(--color-status-green)22; color: var(--color-status-green); }
    .columns {
        display: flex;
        flex: 1;
        min-height: 0;
        overflow: hidden;
    }
    .column {
        display: flex;
        flex-direction: column;
        min-height: 0;
        overflow: hidden;
    }
    .column-context {
        flex-shrink: 0;
        border-right: 1px solid var(--color-border);
        background: var(--color-surface-0);
    }
    .task-list {
        flex: 1;
        overflow-y: auto;
        padding: 8px 6px 6px 6px;
        display: flex;
        flex-direction: column;
        gap: 8px;
    }
    .section-group {
        background: var(--color-surface-1);
        border-radius: var(--radius-lg);
        overflow: hidden;
    }
    .section-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 10px 12px;
        cursor: pointer;
        background: transparent;
        border: none;
        border-bottom: 2px solid var(--col-color);
        width: 100%;
        text-align: left;
        font: inherit;
        color: inherit;
    }
    .section-header:hover {
        background: var(--color-surface-2);
    }
    .section-title {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: 0.8rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.4px;
        color: var(--color-text-secondary);
        flex: 1;
        min-width: 0;
        overflow: hidden;
        text-overflow: ellipsis;
    }
    .section-title .col-icon {
        flex-shrink: 0;
        color: var(--col-color);
    }
    .section-count {
        background: var(--color-surface-3);
        border-radius: 10px;
        padding: 2px 8px;
        font-size: 0.75rem;
        color: var(--color-text-secondary);
        flex-shrink: 0;
        margin-left: 8px;
    }
    .section-chevron {
        flex-shrink: 0;
        margin-left: 4px;
        color: var(--color-text-tertiary);
        transition: transform 0.2s ease;
    }
    .section-header[aria-expanded="false"] .section-chevron {
        transform: rotate(-90deg);
    }
    .section-body {
        padding: 4px 6px 6px 6px;
        display: flex;
        flex-direction: column;
        gap: 2px;
    }
    .task-item {
        padding: 8px 10px;
        border-radius: var(--radius-sm);
        cursor: pointer;
        display: flex;
        justify-content: space-between;
        align-items: center;
        gap: 8px;
        transition: background 0.1s ease;
        text-align: left;
        width: 100%;
        font: inherit;
        color: inherit;
        background: none;
        border: none;
        box-sizing: border-box;
    }
    .task-item:hover { background: var(--color-surface-2); }
    .task-item.selected {
        background: var(--color-accent)18;
        outline: 1px solid var(--color-accent);
        outline-offset: 1px;
        color: var(--color-accent);
        font-weight: 600;
    }
    .task-item-title {
        font-size: 0.75rem;
        color: var(--color-text-primary);
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
        flex: 1;
        min-width: 0;
    }
    .task-item.selected .task-item-title {
        color: var(--color-accent);
    }
    .task-item-edit-btn {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 28px;
        height: 28px;
        padding: 0;
        background: transparent;
        border: none;
        border-radius: var(--radius-sm);
        color: var(--color-text-tertiary);
        cursor: pointer;
        opacity: 0;
        transition: opacity 0.15s ease, background 0.15s ease;
        flex-shrink: 0;
    }
    .task-item:hover .task-item-edit-btn,
    .task-item.selected .task-item-edit-btn {
        opacity: 1;
    }
    .task-item-edit-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    @media (hover: none) {
        .task-item-edit-btn {
            opacity: 1;
            transition: background 0.15s ease;
        }
    }
    .task-item-editing {
        padding: 4px 6px;
    }
    .task-item-title-input {
        width: 100%;
        font-size: 0.75rem;
        font-weight: 600;
        color: var(--color-text-primary);
        background: var(--color-surface-1);
        border: 1px solid var(--color-accent);
        border-radius: var(--radius-sm);
        padding: 3px 6px;
        font-family: inherit;
        outline: none;
    }
    .task-item-badge {
        font-size: 0.75rem;
        font-weight: 600;
        text-transform: uppercase;
        flex-shrink: 0;
    }
    .column-chat {
        min-width: 0;
        border-right: 1px solid var(--color-border);
    }
    .column-diff {
        min-width: 0;
    }
    .btn-icon-sm:hover { background: var(--color-surface-3); color: var(--color-text-primary); }
    .btn-icon-sm:disabled { opacity: 0.4; cursor: not-allowed; }
    .diff-content {
        flex: 1;
        display: flex;
        flex-direction: column;
        min-height: 0;
        overflow: hidden;
    }
    .mobile-tab-bar {
        display: none;
    }
    .column.hidden {
        display: none;
    }

    @media (max-width: 768px) {
        .mobile-tab-bar {
            display: flex;
            border-bottom: 1px solid var(--color-border);
            background: var(--color-surface-1);
            flex-shrink: 0;
        }
        .tab-btn {
            flex: 1;
            display: flex;
            align-items: center;
            justify-content: center;
            gap: 4px;
            padding: 10px 8px;
            border: none;
            background: none;
            color: var(--color-text-tertiary);
            font-size: 0.75rem;
            font-weight: 500;
            cursor: pointer;
            border-bottom: 2px solid transparent;
            transition: all 0.15s ease;
        }
        .tab-btn.active {
            color: var(--color-accent);
            border-bottom-color: var(--color-accent);
        }
        .tab-btn:hover:not(.active) {
            color: var(--color-text-primary);
        }
        .columns.mobile-swipe {
            overflow-x: auto;
            overflow-y: hidden;
            scroll-snap-type: x mandatory;
            -webkit-overflow-scrolling: touch;
        }
        .columns.mobile-swipe .column {
            flex: 0 0 100% !important;
            min-width: 100% !important;
            width: 100% !important;
            scroll-snap-align: start;
        }
        .column-context {
            border-right: none;
        }
        .column-chat {
            border-right: none;
        }
        .task-list {
            padding: 0 8px 8px 8px;
        }
        .section-header {
            padding: 12px 10px;
        }
        .section-body {
            padding: 6px 8px 8px 8px;
        }
        .task-item {
            padding: 10px 12px;
            min-height: 44px;
            align-items: center;
        }
        .task-item-title {
            font-size: 0.875rem;
        }
    }
</style>
