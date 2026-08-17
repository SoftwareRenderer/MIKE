import { describe, it, expect, afterEach } from 'vitest';
import { resolveDetailColumn, resolveDiffFileTarget, type DetailColumnId } from '../use-task-detail-column-nav';

// Pure resolvers for task-view column nav; DOM fixtures mirror CardDetail.
const ORDER: DetailColumnId[] = ['board', 'chat', 'diff'];

describe('resolveDetailColumn', () => {
    it('Right cycles board -> chat -> diff -> board', () => {
        expect(resolveDetailColumn(ORDER, 'board', 'ArrowRight')).toBe('chat');
        expect(resolveDetailColumn(ORDER, 'chat', 'ArrowRight')).toBe('diff');
        expect(resolveDetailColumn(ORDER, 'diff', 'ArrowRight')).toBe('board');
    });

    it('Left cycles board -> diff -> chat -> board', () => {
        expect(resolveDetailColumn(ORDER, 'board', 'ArrowLeft')).toBe('diff');
        expect(resolveDetailColumn(ORDER, 'diff', 'ArrowLeft')).toBe('chat');
        expect(resolveDetailColumn(ORDER, 'chat', 'ArrowLeft')).toBe('board');
    });

    it('neutral focus (null) anchors: Right -> first, Left -> last', () => {
        expect(resolveDetailColumn(ORDER, null, 'ArrowRight')).toBe('board');
        expect(resolveDetailColumn(ORDER, null, 'ArrowLeft')).toBe('diff');
    });

    it('current outside the given order is treated as neutral', () => {
        // A subset order (e.g. a disabled column) — 'diff' is not in the order.
        expect(resolveDetailColumn(ORDER.slice(0, 2), 'diff', 'ArrowRight')).toBe('board');
        expect(resolveDetailColumn(ORDER.slice(0, 2), 'diff', 'ArrowLeft')).toBe('chat');
    });

    it('returns null for an empty order', () => {
        expect(resolveDetailColumn([], 'board', 'ArrowRight')).toBeNull();
    });
});

describe('resolveDiffFileTarget', () => {
    let sections: HTMLElement[] = [];
    let headers: HTMLElement[] = [];
    let ids: string[] = [];

    function makeFileSection(i: number) {
        const section = document.createElement('section');
        section.className = 'diff-file-section';
        const header = document.createElement('button');
        header.className = 'diff-file-header';
        const id = `f${i + 1}`;
        header.dataset.id = id;
        ids.push(id);
        section.appendChild(header);
        document.body.appendChild(section);
        sections.push(section);
        headers.push(header);
        return { section, header };
    }

    const header = (id: string) => headers.find(h => h.dataset.id === id)!;
    const bodyOf = (i: number) => {
        const body = document.createElement('div');
        body.className = 'diff-file-body';
        sections[i].appendChild(body);
        return body;
    };

    afterEach(() => {
        document.body.innerHTML = '';
        sections = [];
        headers = [];
        ids = [];
    });

    it('Down from the last header wraps to the first', () => {
        for (let i = 0; i < 3; i++) makeFileSection(i);
        expect(resolveDiffFileTarget({ sections, headers, activeElement: header('f3'), key: 'ArrowDown' })).toBe(header('f1'));
    });

    it('Up from the first header wraps to the last', () => {
        for (let i = 0; i < 3; i++) makeFileSection(i);
        expect(resolveDiffFileTarget({ sections, headers, activeElement: header('f1'), key: 'ArrowUp' })).toBe(header('f3'));
    });

    it('Down/Up move to the next/prev header from a focused header', () => {
        for (let i = 0; i < 3; i++) makeFileSection(i);
        expect(resolveDiffFileTarget({ sections, headers, activeElement: header('f1'), key: 'ArrowDown' })).toBe(header('f2'));
        expect(resolveDiffFileTarget({ sections, headers, activeElement: header('f2'), key: 'ArrowUp' })).toBe(header('f1'));
    });

    it('focus inside a file body anchors to that file (Down -> next header, Up -> previous)', () => {
        for (let i = 0; i < 3; i++) makeFileSection(i);
        const body1 = bodyOf(0);
        const body3 = bodyOf(2);
        expect(resolveDiffFileTarget({ sections, headers, activeElement: body1, key: 'ArrowDown' })).toBe(header('f2'));
        expect(resolveDiffFileTarget({ sections, headers, activeElement: body3, key: 'ArrowUp' })).toBe(header('f2'));
    });

    it('neutral focus anchors: Down -> first header, Up -> last header', () => {
        for (let i = 0; i < 2; i++) makeFileSection(i);
        const column = document.createElement('div');
        column.className = 'column-diff';
        document.body.appendChild(column);
        expect(resolveDiffFileTarget({ sections, headers, activeElement: column, key: 'ArrowDown' })).toBe(header('f1'));
        expect(resolveDiffFileTarget({ sections, headers, activeElement: column, key: 'ArrowUp' })).toBe(header('f2'));
    });

    it('returns null when there are no headers', () => {
        expect(resolveDiffFileTarget({ sections: [], headers: [], activeElement: null, key: 'ArrowDown' })).toBeNull();
    });
});
