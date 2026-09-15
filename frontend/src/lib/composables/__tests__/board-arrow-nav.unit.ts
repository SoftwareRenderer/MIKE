import { describe, it, expect, afterEach } from 'vitest';
import { resolveArrowTarget, resolveOverviewArrowTarget, type BoardColumnRef } from '../use-board-arrow-nav';

// Fixture: project list pseudo-column (left of backlog) + 4 board columns.
// Ring: [Projects] [Backlog] [In Progress] [Review] [Done] (cyclic).
let pIds: string[] = [];
let tIds: string[] = [];

function makeProjectItems(count: number): { nav: HTMLElement; items: HTMLElement[] } {
    const nav = document.createElement('nav');
    nav.className = 'project-list';
    const items: HTMLElement[] = [];
    for (let i = 0; i < count; i++) {
        const id = `p${tIds.length + pIds.length + 1}`;
        pIds.push(id);
        const wrapper = document.createElement('div');
        wrapper.className = 'project-item-wrapper';
        const btn = document.createElement('button');
        btn.className = 'project-item';
        btn.dataset.id = id;
        btn.textContent = id;
        wrapper.appendChild(btn);
        nav.appendChild(wrapper);
        items.push(btn);
    }
    return { nav, items };
}

function makeColumn(count: number): HTMLDivElement {
    const col = document.createElement('div');
    col.className = 'column';
    const list = document.createElement('div');
    list.className = 'card-list';
    for (let i = 0; i < count; i++) {
        const id = `t${tIds.length + 1}`;
        tIds.push(id);
        const wrapper = document.createElement('div');
        wrapper.className = 'task-wrapper';
        const card = document.createElement('div');
        card.className = 'card';
        card.dataset.id = id;
        card.tabIndex = 0;
        wrapper.appendChild(card);
        list.appendChild(wrapper);
    }
    col.appendChild(list);
    return col;
}

/** cols: [backlog, inProgress, review, done]; projects: project-item count. */
function setupBoard(backlog: number, inProgress: number, review: number, done: number, projects = 1): BoardColumnRef[] {
    document.body.innerHTML = '';
    pIds = [];
    tIds = [];
    const cols: BoardColumnRef[] = [];
    if (projects > 0) {
        const { nav, items } = makeProjectItems(projects);
        document.body.appendChild(nav);
        cols.push({ el: nav, cards: items, isProjects: true });
    }
    const board = document.createElement('div');
    board.className = 'board';
    for (const n of [backlog, inProgress, review, done]) {
        const col = makeColumn(n);
        board.appendChild(col);
        cols.push({
            el: col,
            cards: Array.from(col.querySelectorAll<HTMLElement>(':scope > .card-list > .task-wrapper > .card')),
        });
    }
    document.body.appendChild(board);
    return cols;
}

const card = (id: string) => document.querySelector<HTMLElement>(`.card[data-id="${id}"]`)!;
const proj = (id: string) => document.querySelector<HTMLElement>(`.project-item[data-id="${id}"]`)!;
const idOf = (el: Element | null) => el?.getAttribute('data-id') ?? el?.closest('.card, .project-item')?.getAttribute('data-id') ?? null;

afterEach(() => { document.body.innerHTML = ''; });

describe('resolveArrowTarget — neutral (no card focused)', () => {
    it('Down and Up anchor to the first backlog card, never the projects list', () => {
        const cols = setupBoard(2, 1, 0, 1, 2);
        expect(idOf(resolveArrowTarget({ columns: cols, activeElement: null, key: 'ArrowDown' }))).toBe('t1');
        expect(idOf(resolveArrowTarget({ columns: cols, activeElement: null, key: 'ArrowUp' }))).toBe('t1');
    });

    it('Left/Right anchor to the first backlog card, never the projects list', () => {
        const cols = setupBoard(2, 1, 0, 1, 2);
        expect(idOf(resolveArrowTarget({ columns: cols, activeElement: null, key: 'ArrowLeft' }))).toBe('t1');
        expect(idOf(resolveArrowTarget({ columns: cols, activeElement: null, key: 'ArrowRight' }))).toBe('t1');
    });

    it('falls back to the first non-empty board column when backlog is empty', () => {
        const cols = setupBoard(0, 1, 0, 1);
        for (const key of ['ArrowDown', 'ArrowUp', 'ArrowLeft', 'ArrowRight'] as const) {
            expect(idOf(resolveArrowTarget({ columns: cols, activeElement: null, key }))).toBe('t1');
        }
    });

    it('returns null when every board column is empty (projects have items)', () => {
        const cols = setupBoard(0, 0, 0, 0, 1);
        for (const key of ['ArrowDown', 'ArrowUp', 'ArrowLeft', 'ArrowRight'] as const) {
            expect(resolveArrowTarget({ columns: cols, activeElement: null, key })).toBeNull();
        }
    });
});

describe('resolveArrowTarget — Up/Down within a column (wrap both ends)', () => {
    it('moves to next/prev card', () => {
        const cols = setupBoard(3, 1, 0, 0);
        // t1..t3 backlog, t4 in_progress
        expect(idOf(resolveArrowTarget({ columns: cols, activeElement: card('t2'), key: 'ArrowDown' }))).toBe('t3');
        expect(idOf(resolveArrowTarget({ columns: cols, activeElement: card('t3'), key: 'ArrowUp' }))).toBe('t2');
    });

    it('wraps last -> first and first -> last', () => {
        const cols = setupBoard(3, 0, 0, 0);
        expect(idOf(resolveArrowTarget({ columns: cols, activeElement: card('t3'), key: 'ArrowDown' }))).toBe('t1');
        expect(idOf(resolveArrowTarget({ columns: cols, activeElement: card('t1'), key: 'ArrowUp' }))).toBe('t3');
    });

    it('stays put in a single-card column', () => {
        const cols = setupBoard(1, 1, 0, 0);
        expect(idOf(resolveArrowTarget({ columns: cols, activeElement: card('t1'), key: 'ArrowDown' }))).toBe('t1');
        expect(idOf(resolveArrowTarget({ columns: cols, activeElement: card('t1'), key: 'ArrowUp' }))).toBe('t1');
    });

    it('does not leave the current column', () => {
        const cols = setupBoard(1, 1, 0, 0);
        expect(idOf(resolveArrowTarget({ columns: cols, activeElement: card('t1'), key: 'ArrowDown' }))).toBe('t1');
    });

    it('moves between project items (focus stays in the projects column)', () => {
        const cols = setupBoard(1, 0, 0, 0, 2);
        expect(idOf(resolveArrowTarget({ columns: cols, activeElement: proj('p1'), key: 'ArrowDown' }))).toBe('p2');
        expect(idOf(resolveArrowTarget({ columns: cols, activeElement: proj('p2'), key: 'ArrowDown' }))).toBe('p1'); // wrap
        expect(idOf(resolveArrowTarget({ columns: cols, activeElement: proj('p2'), key: 'ArrowUp' }))).toBe('p1');
    });
});

describe('resolveArrowTarget — Left/Right across the 5-column ring', () => {
    it('Right goes to the first card of the next board column', () => {
        const cols = setupBoard(1, 2, 0, 0); // t1 bl, t2 t3 ip
        expect(idOf(resolveArrowTarget({ columns: cols, activeElement: card('t1'), key: 'ArrowRight' }))).toBe('t2');
    });

    it('Left on backlog enters the projects list', () => {
        const cols = setupBoard(1, 1, 1, 1, 2);
        expect(idOf(resolveArrowTarget({ columns: cols, activeElement: card('t1'), key: 'ArrowLeft' }))).toBe('p1');
    });

    it('Right on done wraps to the projects list', () => {
        const cols = setupBoard(1, 1, 1, 1, 2); // t1 bl, t2 ip, t3 rev, t4 done
        expect(idOf(resolveArrowTarget({ columns: cols, activeElement: card('t4'), key: 'ArrowRight' }))).toBe('p1');
    });

    it('Left on a project wraps to the first done card', () => {
        const cols = setupBoard(1, 1, 1, 2, 1); // t1 bl, t2 ip, t3 rev, t4 t5 done
        expect(idOf(resolveArrowTarget({ columns: cols, activeElement: proj('p1'), key: 'ArrowLeft' }))).toBe('t4');
    });

    it('Right on a project goes to the first backlog card', () => {
        const cols = setupBoard(2, 1, 0, 1, 1);
        expect(idOf(resolveArrowTarget({ columns: cols, activeElement: proj('p1'), key: 'ArrowRight' }))).toBe('t1');
    });

    it('skips empty columns', () => {
        const cols = setupBoard(1, 1, 0, 1); // t1 bl, t2 ip, t3 done; review empty
        expect(idOf(resolveArrowTarget({ columns: cols, activeElement: card('t2'), key: 'ArrowRight' }))).toBe('t3');
        expect(idOf(resolveArrowTarget({ columns: cols, activeElement: card('t3'), key: 'ArrowLeft' }))).toBe('t2');
    });

    it('reaches the projects list across empty columns', () => {
        const cols = setupBoard(0, 1, 0, 0, 1); // only in_progress has cards (t1)
        expect(idOf(resolveArrowTarget({ columns: cols, activeElement: card('t1'), key: 'ArrowLeft' }))).toBe('p1');
    });

    it('returns null when no other column has cards', () => {
        const cols = setupBoard(1, 0, 0, 0, 0);
        expect(resolveArrowTarget({ columns: cols, activeElement: card('t1'), key: 'ArrowRight' })).toBeNull();
        expect(resolveArrowTarget({ columns: cols, activeElement: card('t1'), key: 'ArrowLeft' })).toBeNull();
    });

    it('always lands on the FIRST card of the target column', () => {
        const cols = setupBoard(2, 3, 0, 0); // t1 t2 bl, t3 t4 t5 ip
        expect(idOf(resolveArrowTarget({ columns: cols, activeElement: card('t2'), key: 'ArrowRight' }))).toBe('t3');
        expect(idOf(resolveArrowTarget({ columns: cols, activeElement: card('t5'), key: 'ArrowLeft' }))).toBe('t1');
    });
});

describe('resolveArrowTarget — focus inside a column but not on a card', () => {
    it('Down -> first card, Up -> last card of that column', () => {
        const cols = setupBoard(2, 2, 0, 0); // t1 t2 bl, t3 t4 ip
        const header = document.createElement('button');
        (cols[2].el.querySelector('.card-list') as HTMLElement).before(header);
        expect(idOf(resolveArrowTarget({ columns: cols, activeElement: header, key: 'ArrowDown' }))).toBe('t3');
        expect(idOf(resolveArrowTarget({ columns: cols, activeElement: header, key: 'ArrowUp' }))).toBe('t4');
    });
});

// ── resolveOverviewArrowTarget — task-view board overview (single vertical list) ──

let ovIds: string[] = [];

/** Build a `.task-list` of overview cards across section groups; returns the card list. */
function setupOverview(perSection: number[]): HTMLElement[] {
    document.body.innerHTML = '';
    ovIds = [];
    const list = document.createElement('div');
    list.className = 'task-list';
    perSection.forEach((count) => {
        const group = document.createElement('div');
        group.className = 'section-group';
        const body = document.createElement('div');
        body.className = 'section-body';
        for (let i = 0; i < count; i++) {
            const id = `ov${ovIds.length + 1}`;
            ovIds.push(id);
            const wrapper = document.createElement('div');
            wrapper.className = 'task-wrapper';
            const c = document.createElement('div');
            c.className = 'card';
            c.dataset.id = id;
            c.tabIndex = 0;
            wrapper.appendChild(c);
            body.appendChild(wrapper);
        }
        group.appendChild(body);
        list.appendChild(group);
    });
    document.body.appendChild(list);
    // Query from an ancestor (as CardDetail does from detailEl), not from
    // .task-list itself — the selector needs .task-list as a descendant.
    return Array.from(document.querySelectorAll<HTMLElement>('.task-list .task-wrapper > .card'));
}

const ovCard = (id: string) => document.querySelector<HTMLElement>(`.card[data-id="${id}"]`)!;

describe('resolveOverviewArrowTarget', () => {
    it('moves to next/prev card', () => {
        const cards = setupOverview([2, 2]); // ov1 ov2 | ov3 ov4
        expect(ovCard('ov2')).toBe(resolveOverviewArrowTarget({ cards, activeElement: ovCard('ov1'), key: 'ArrowDown' }));
        expect(ovCard('ov2')).toBe(resolveOverviewArrowTarget({ cards, activeElement: ovCard('ov3'), key: 'ArrowUp' }));
    });

    it('moves across section boundaries in DOM order', () => {
        const cards = setupOverview([2, 2]);
        expect(ovCard('ov3')).toBe(resolveOverviewArrowTarget({ cards, activeElement: ovCard('ov2'), key: 'ArrowDown' }));
        expect(ovCard('ov2')).toBe(resolveOverviewArrowTarget({ cards, activeElement: ovCard('ov3'), key: 'ArrowUp' }));
    });

    it('wraps last -> first and first -> last', () => {
        const cards = setupOverview([2, 2]);
        expect(ovCard('ov1')).toBe(resolveOverviewArrowTarget({ cards, activeElement: ovCard('ov4'), key: 'ArrowDown' }));
        expect(ovCard('ov4')).toBe(resolveOverviewArrowTarget({ cards, activeElement: ovCard('ov1'), key: 'ArrowUp' }));
    });

    it('stays put in a single-card list', () => {
        const cards = setupOverview([1]);
        expect(ovCard('ov1')).toBe(resolveOverviewArrowTarget({ cards, activeElement: ovCard('ov1'), key: 'ArrowDown' }));
        expect(ovCard('ov1')).toBe(resolveOverviewArrowTarget({ cards, activeElement: ovCard('ov1'), key: 'ArrowUp' }));
    });

    it('anchors to the first card when nothing is focused', () => {
        const cards = setupOverview([1, 2]);
        expect(ovCard('ov1')).toBe(resolveOverviewArrowTarget({ cards, activeElement: null, key: 'ArrowDown' }));
        expect(ovCard('ov1')).toBe(resolveOverviewArrowTarget({ cards, activeElement: null, key: 'ArrowUp' }));
    });

    it('anchors to the first card when focus is on a non-card element', () => {
        const cards = setupOverview([2]);
        const header = document.createElement('button');
        header.className = 'section-header';
        document.body.appendChild(header);
        expect(ovCard('ov1')).toBe(resolveOverviewArrowTarget({ cards, activeElement: header, key: 'ArrowDown' }));
        expect(ovCard('ov1')).toBe(resolveOverviewArrowTarget({ cards, activeElement: header, key: 'ArrowUp' }));
    });

    it('matches focus inside a card element (e.g. a card child button)', () => {
        const cards = setupOverview([2]); // ov1, ov2
        const inner = document.createElement('button');
        ovCard('ov2').appendChild(inner);
        expect(ovCard('ov1')).toBe(resolveOverviewArrowTarget({ cards, activeElement: inner, key: 'ArrowUp' }));
    });

    it('returns null for an empty list', () => {
        const cards = setupOverview([]);
        expect(resolveOverviewArrowTarget({ cards, activeElement: null, key: 'ArrowDown' })).toBeNull();
    });
});
