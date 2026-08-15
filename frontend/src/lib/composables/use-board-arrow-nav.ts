import { onMount } from 'svelte';
import { isEditableTarget } from '$lib/utils/keyboard';

/**
 * Arrow-key navigation over the 5-column ring [Projects][Backlog][In Progress]
 * [Review][Done]. Moves real DOM focus (the :focus-visible ring is the
 * highlight; Enter/Alt+Arrows are handled by the focused element itself).
 * Focus inside `.project-list` is owned by App/Sidebar.svelte, so skipped here.
 */

export interface BoardColumnRef {
    /** The `.column` element (project-list nav for the pseudo-column). */
    el: Element;
    /** Top-level `.card` elements (project items for the pseudo-column). */
    cards: HTMLElement[];
    /** True for the project-list pseudo-column (left of backlog). */
    isProjects?: boolean;
}

export type ArrowKey = 'ArrowDown' | 'ArrowUp' | 'ArrowLeft' | 'ArrowRight';

/**
 * Pure navigation step (unit-tested): Down/Up move within the focused column
 * (wrapping; neutral anchors to the first non-empty board column, never the
 * projects list); Left/Right jump to the first card of the next non-empty
 * column, cycling through the ring. Returns null when there is no target.
 */
export function resolveArrowTarget(opts: {
    columns: BoardColumnRef[];
    activeElement: Element | null;
    key: ArrowKey;
}): HTMLElement | null {
    const { columns, activeElement, key } = opts;
    if (!columns.length) return null;

    let colIdx = -1;
    let cardIdx = -1;
    if (activeElement instanceof Element) {
        colIdx = columns.findIndex((c) => c.el === activeElement || c.el.contains(activeElement));
        if (colIdx >= 0) cardIdx = columns[colIdx].cards.findIndex((c) => c.contains(activeElement));
    }

    if (key === 'ArrowDown' || key === 'ArrowUp') {
        const dir = key === 'ArrowDown' ? 1 : -1;
        if (colIdx === -1) {
            for (const col of columns) if (!col.isProjects && col.cards.length) return col.cards[0];
            return null;
        }
        const cards = columns[colIdx].cards;
        if (!cards.length) return null;
        if (cardIdx === -1) return dir > 0 ? cards[0] : cards[cards.length - 1];
        return cards[(cardIdx + dir + cards.length) % cards.length];
    }

    if (colIdx === -1) return null;
    const dir = key === 'ArrowRight' ? 1 : -1;
    // steps 1..n-1: every other column exactly once, cyclic through projects
    for (let step = 1; step < columns.length; step++) {
        const ci = (((colIdx + dir * step) % columns.length) + columns.length) % columns.length;
        if (columns[ci].cards.length) return columns[ci].cards[0];
    }
    return null;
}

/** Build the column refs from the live DOM (projects pseudo-column first). */
function collectNavColumns(): BoardColumnRef[] {
    const cols: BoardColumnRef[] = [];
    const listNav = document.querySelector('nav.project-list');
    if (listNav) {
        const items = Array.from(listNav.querySelectorAll<HTMLElement>('button.project-item'));
        if (items.length) cols.push({ el: listNav, cards: items, isProjects: true });
    }
    const board = document.querySelector('.board');
    if (!board) return cols;
    for (const el of Array.from(board.querySelectorAll<HTMLElement>('.column'))) {
        cols.push({
            el,
            cards: Array.from(el.querySelectorAll<HTMLElement>(':scope > .card-list > .task-wrapper > .card')),
        });
    }
    return cols;
}

/** First top-level card of the first (or last) board column, or null. */
export function boardEdgeCard(edge: 'first' | 'last'): HTMLElement | null {
    const board = document.querySelector('.board');
    if (!board) return null;
    const columns = Array.from(board.querySelectorAll<HTMLElement>('.column'));
    const col = edge === 'first' ? columns[0] : columns[columns.length - 1];
    return col?.querySelector<HTMLElement>(':scope > .card-list > .task-wrapper > .card') ?? null;
}

/** Entering the project list always lands on the active project (fallback: first). */
function activeProjectItem(): HTMLElement | null {
    return (
        document.querySelector<HTMLElement>('nav.project-list .project-item-wrapper.active button.project-item') ??
        document.querySelector<HTMLElement>('nav.project-list button.project-item')
    );
}

export function useBoardArrowNav({ isEnabled }: { isEnabled: () => boolean }) {
    onMount(() => {
        function handleKeydown(e: KeyboardEvent) {
            if (!isEnabled()) return;
            if (e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
            if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp' && e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
            const active = document.activeElement;
            if (active instanceof Element && (active.closest('.project-list') || isEditableTarget(active))) return;
            let target = resolveArrowTarget({ columns: collectNavColumns(), activeElement: active, key: e.key });
            if (target && target.closest('nav.project-list')) target = activeProjectItem() ?? target;
            if (!target) return;
            e.preventDefault();
            target.focus();
            target.scrollIntoView({ block: 'nearest', inline: 'nearest' });
        }
        window.addEventListener('keydown', handleKeydown);
        return () => window.removeEventListener('keydown', handleKeydown);
    });
}
