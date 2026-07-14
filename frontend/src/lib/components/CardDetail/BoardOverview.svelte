<script lang="ts">
    import { fade, slide } from 'svelte/transition';
    import type { Task } from '$lib/types';
    import Card from '../Card.svelte';
    import { sortByActivity } from '$lib/utils/sort-tasks';

    let {
        sections,
        allTasks,
        consolidatedSubtaskIds,
        subtaskIds,
        sectionDragState,
        collapsedSections,
        currentTask,
        childrenMap,
        expandedTasks,
        onSelectTask,
        onRefreshBoard,
        onToggleSection,
        onSectionDragOver,
        onSectionDragLeave,
        onSectionDrop,
        onClearDone,
        onToggleExpanded,
        onCreateChildTask,
        onShowCreateTask,
        getColumnColor,
    }: {
        sections: Array<{ id: string; title: string }>;
        allTasks: Task[];
        consolidatedSubtaskIds: Set<string>;
        subtaskIds: Set<string>;
        sectionDragState: Map<string, { isOver: boolean; isRejected: boolean }>;
        collapsedSections: Set<string>;
        currentTask: Task;
        childrenMap: Map<string, Task[]>;
        expandedTasks: Set<string>;
        onSelectTask?: (task: Task) => void;
        onRefreshBoard?: () => void;
        onToggleSection: (sectionId: string) => void;
        onSectionDragOver: (e: DragEvent, sectionId: string) => void;
        onSectionDragLeave: (sectionId: string) => void;
        onSectionDrop: (e: DragEvent, sectionId: string) => void;
        onClearDone: (count: number) => Promise<void>;
        onToggleExpanded: (taskId: string) => void;
        onCreateChildTask: (parentId: string) => void;
        onShowCreateTask: () => void;

        getColumnColor: (sectionId: string) => string;
    } = $props();

    // Scroll to current task when task detail opens or task changes.
    // The current task's section is always expanded (handled in template).
    $effect(() => {
        const taskId = currentTask.id;
        if (!taskId) return;
        // Scroll into view after DOM is painted
        requestAnimationFrame(() => {
            requestAnimationFrame(() => {
                const card = document.querySelector(`.card[data-task-id="${taskId}"]`);
                if (card) {
                    card.scrollIntoView({ behavior: 'auto', block: 'center' });
                }
            });
        });
    });
</script>

<div class="task-list">
    {#each sections as section (section.id)}
        {@const sectionTasks = sortByActivity(allTasks.filter(t => { if (t.status !== section.id) return false; if (consolidatedSubtaskIds.has(t.id)) return false; if (!subtaskIds.has(t.id)) return true; return true; }))}
        {@const sectionState = sectionDragState.get(section.id) || { isOver: false, isRejected: false }}
        <div
            class="section-group"
            class:is-over={sectionState.isOver}
            class:is-drop-rejected={sectionState.isRejected}
            style="--col-color: {getColumnColor(section.id)}"
            role="region"
            ondragover={(e) => onSectionDragOver(e, section.id)}
            ondragleave={() => onSectionDragLeave(section.id)}
            ondrop={(e) => onSectionDrop(e, section.id)}
        >
            <button class="section-header" onclick={() => section.id !== currentTask.status && onToggleSection(section.id)} type="button" aria-expanded={section.id === currentTask.status || !collapsedSections.has(section.id)} title={section.id === currentTask.status ? `${section.title} (current task)` : (collapsedSections.has(section.id) ? `Expand ${section.title}` : `Collapse ${section.title}`)}>
                <span class="section-title">
                    {#if section.id === 'backlog'}
                        <svg class="col-icon" width="12" height="12" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" stroke="currentColor" stroke-width="1.5"/></svg>
                    {:else if section.id === 'in_progress'}
                        <svg class="col-icon" width="12" height="12" viewBox="0 0 10 10" fill="none" aria-hidden="true"><polygon points="3.5,1.5 3.5,8.5 8.5,5" fill="currentColor"/></svg>
                    {:else if section.id === 'review'}
                        <svg class="col-icon" width="12" height="12" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" stroke="currentColor" stroke-width="1"/><circle cx="5" cy="5" r="1.2" fill="currentColor"/></svg>
                    {:else if section.id === 'done'}
                        <svg class="col-icon" width="12" height="12" viewBox="0 0 10 10" fill="none" aria-hidden="true"><circle cx="5" cy="5" r="3.5" fill="currentColor"/></svg>
                    {/if}
                    {section.title}
                </span>
                <div class="header-right">
                    <span class="section-count">{sectionTasks.length}</span>
                    {#if section.id === 'backlog'}
                        <span class="add-btn" tabindex="0" role="button" onclick={(e) => { e.stopPropagation(); onShowCreateTask(); }} onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); e.stopPropagation(); onShowCreateTask(); } }} aria-label="Create a new task" title="Create a new task">
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M8 3v10M3 8h10" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>
                        </span>
                    {/if}
                    {#if section.id === 'done' && sectionTasks.length > 0}
                        <span class="clear-btn" tabindex="0" role="button" onclick={(e) => { e.stopPropagation(); onClearDone(sectionTasks.length); }} onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); e.stopPropagation(); onClearDone(sectionTasks.length); } }} aria-label={`Clear all ${sectionTasks.length} done tasks`} title={`Clear all ${sectionTasks.length} done tasks`}>
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                                <path d="M2 4h12M5 4V3a1 1 0 011-1h4a1 1 0 011 1v1M6 7v5M10 7v5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                                <path d="M3 4l1 9a1 1 0 001 1h6a1 1 0 001-1l1-9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                            </svg>
                        </span>
                    {/if}
                </div>
                <svg class="section-chevron" width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
            </button>
            {#if section.id === currentTask.status || !collapsedSections.has(section.id)}
            <div class="section-body" transition:slide={{ duration: 200 }}>
                {#each sectionTasks as t (t.id)}
                    {@const selectHandler = (selected: Task) => { if (onSelectTask) onSelectTask(selected); }}
                    <div class="task-wrapper" transition:fade={{ duration: 200 }}>
                        <Card
                            task={t}
                            columnId={section.id}
                            projectId={currentTask.project_id}
                            childrenMap={childrenMap}
                            expandedTasks={expandedTasks}
                            onToggleExpanded={onToggleExpanded}
                            onCreateChildTask={onCreateChildTask}
                            columnColor={getColumnColor(section.id)}
                            inline={false}
                            showSubtaskBorder={true}
                            onSelect={selectHandler}
                            onRefreshBoard={onRefreshBoard}
                        />
                    </div>
                {/each}
                {#if section.id === 'backlog'}
                    <button class="task-item-add" onclick={onShowCreateTask} type="button" title="Create a new task">+ Create Task</button>
                {/if}
            </div>
            {/if}
        </div>
    {/each}
</div>

<style>
    .task-list {
        display: flex;
        flex-direction: column;
        gap: 12px;
        padding: 12px;
    }
    .section-group {
        background: var(--color-surface-1);
        border-top: 1px solid var(--color-border);
        border-left: 3px solid var(--col-color);
        border-radius: var(--radius-md);
        transition: border-color 0.15s ease, box-shadow 0.15s ease;
    }
    .section-group.is-over {
        border-color: var(--color-accent);
        box-shadow: 0 0 0 2px var(--color-accent)40;
    }
    .section-group.is-drop-rejected {
        border-color: var(--color-status-red, #f85149);
        box-shadow: 0 0 0 2px var(--color-status-red, #f85149)40;
    }
    .section-header {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 8px 12px;
        background-color: var(--color-surface-1);
        border: none;
        cursor: pointer;
        color: var(--color-text-secondary);
        font-size: 0.875rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.5px;
        width: 100%;
        text-align: left;
        position: sticky;
        top: 0;
        z-index: 5;
        -webkit-appearance: none;
        appearance: none;
    }
    .section-header:hover {
        background-color: var(--color-surface-1);
        opacity: 1;
    }
    .section-title {
        display: flex;
        align-items: center;
        gap: 6px;
        flex: 1;
        min-width: 0;
    }
    .col-icon {
        flex-shrink: 0;
        color: var(--col-color, var(--color-text-secondary));
    }
    .header-right {
        display: flex;
        align-items: center;
        gap: 8px;
    }
    .section-count {
        font-size: 0.75rem;
        color: var(--color-text-secondary);
        background: var(--color-surface-3);
        padding: 1px 6px;
        border-radius: 8px;
        font-weight: 500;
    }
    .add-btn {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        width: 22px;
        height: 22px;
        padding: 0;
        border-radius: var(--radius-sm);
        color: var(--color-text-tertiary);
        cursor: pointer;
        transition: color 0.15s ease, background 0.15s ease;
        opacity: 1;
    }
    .add-btn:hover {
        background: var(--color-accent-dark);
        color: white;
    }
    .add-btn:focus-visible {
        outline: 2px solid var(--color-accent);
        outline-offset: 1px;
    }
    .clear-btn {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        width: 22px;
        height: 22px;
        padding: 0;
        border-radius: var(--radius-sm);
        color: var(--color-text-tertiary);
        cursor: pointer;
        transition: color 0.15s ease, background 0.15s ease;
    }
    .clear-btn:hover {
        background: var(--color-status-red-dark);
        color: white;
    }
    .clear-btn:focus-visible {
        outline: 2px solid var(--color-status-red, #f85149);
        outline-offset: 1px;
    }
    @media (max-width: 768px) {
        .add-btn,
        .clear-btn {
            width: 44px;
            height: 44px;
        }
    }
    .section-chevron {
        flex-shrink: 0;
        transition: transform 0.15s ease;
        color: var(--color-text-tertiary);
    }
    .section-header[aria-expanded="false"] .section-chevron {
        transform: rotate(-90deg);
    }
    .section-body {
        display: flex;
        flex-direction: column;
        gap: 6px;
        padding: 8px;
        overflow: hidden;
        border-radius: 0 0 var(--radius-md) var(--radius-md);
    }
    .task-wrapper {
        animation: fadeIn 0.2s ease;
    }
    @keyframes fadeIn {
        from { opacity: 0; transform: translateY(-4px); }
        to { opacity: 1; transform: translateY(0); }
    }
    .task-item-add {
        display: flex;
        align-items: center;
        justify-content: center;
        padding: 8px;
        border-radius: var(--radius-sm);
        border: 1px dashed var(--color-border);
        background: none;
        color: var(--color-text-tertiary);
        font-size: 0.8125rem;
        cursor: pointer;
        transition: all 0.15s ease;
    }
    .task-item-add:hover {
        border-color: var(--color-accent);
        color: var(--color-accent);
        background: var(--color-accent)10;
    }
</style>
