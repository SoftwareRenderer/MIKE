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
 * Arroy Key navigation
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

    // Neutral anchor (focus outside the ring, e.g. on <body>): first card of
    // the first non-empty board column (backlog first), never the projects list.
    function neutralAnchor(): HTMLElement | null {
        for (const col of columns) if (!col.isProjects && col.cards.length) return col.cards[0];
        return null;
    }

    if (key === 'ArrowDown' || key === 'ArrowUp') {
        const dir = key === 'ArrowDown' ? 1 : -1;
        if (colIdx === -1) return neutralAnchor();
        const cards = columns[colIdx].cards;
        if (!cards.length) return null;
        if (cardIdx === -1) return dir > 0 ? cards[0] : cards[cards.length - 1];
        return cards[(cardIdx + dir + cards.length) % cards.length];
    }

    if (colIdx === -1) return neutralAnchor();
    const dir = key === 'ArrowRight' ? 1 : -1;
    // steps 1..n-1: every other column exactly once, cyclic through projects
    for (let step = 1; step < columns.length; step++) {
        const ci = (((colIdx + dir * step) % columns.length) + columns.length) % columns.length;
        if (columns[ci].cards.length) return columns[ci].cards[0];
    }
    return null;
}

// keyboard nav
export function resolveOverviewArrowTarget(opts: {
    cards: HTMLElement[];
    activeElement: Element | null;
    key: 'ArrowDown' | 'ArrowUp';
}): HTMLElement | null {
    const { cards, activeElement, key } = opts;
    if (!cards.length) return null;
    const idx = activeElement instanceof Element
        ? cards.findIndex((c) => c === activeElement || c.contains(activeElement))
        : -1;
    if (idx === -1) return cards[0];
    const dir = key === 'ArrowDown' ? 1 : -1;
    return cards[(idx + dir + cards.length) % cards.length];
}

/** Build the column refs from the live DOM (projects pseudo-column first). */
function collectNavColumns(): BoardColumnRef[] {
    const cols: BoardColumnRef[] = [];
    const listNav = document.querySelector('nav.project-list');
    if (listNav) {
        // Project rows are anchors, not buttons — select them by class.
        const items = Array.from(listNav.querySelectorAll<HTMLElement>('.project-item'));
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
        document.querySelector<HTMLElement>('nav.project-list .project-item-wrapper.active .project-item') ??
        document.querySelector<HTMLElement>('nav.project-list .project-item')
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
