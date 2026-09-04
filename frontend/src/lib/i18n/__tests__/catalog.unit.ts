import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { parseCatalog, parseCatalogJSON, MAX_CATALOG_KEYS, MAX_VALUE_LEN } from '$lib/i18n/catalog';

describe('parseCatalog (what may enter the runtime)', () => {
    it('accepts a flat object of strings', () => {
        const res = parseCatalog({ 'nav.board': '看板', 'task.create': '新建任务' });
        expect(res.ok).toBe(true);
        if (res.ok) expect(res.catalog['nav.board']).toBe('看板');
    });

    it('accepts an empty catalog — a pack may legitimately translate nothing yet', () => {
        const res = parseCatalog({});
        expect(res.ok).toBe(true);
        if (res.ok) expect(Object.keys(res.catalog)).toEqual([]);
    });

    it('rejects anything that is not an object', () => {
        for (const raw of [null, undefined, [], ['a'], 'text', 42, true]) {
            const res = parseCatalog(raw);
            expect(res.ok).toBe(false);
            if (!res.ok) expect(res.reason).toMatch(/must be a JSON object/);
        }
    });

    it('rejects a nested catalog rather than rendering [object Object]', () => {
        const res = parseCatalog({ nav: { board: '看板' } });
        expect(res.ok).toBe(false);
        if (!res.ok) expect(res.reason).toMatch(/nav.*not a string/);
    });

    it('rejects a null value', () => {
        const res = parseCatalog({ 'a.b': null });
        expect(res.ok).toBe(false);
        if (!res.ok) expect(res.reason).toMatch(/a\.b.*null/);
    });

    it('rejects a prototype-pollution key outright instead of skipping it', () => {
        for (const key of ['__proto__', 'constructor', 'prototype']) {
            const res = parseCatalog(JSON.parse(`{"${key}": "x"}`));
            expect(res.ok).toBe(false);
            if (!res.ok) expect(res.reason).toContain(key);
        }
    });

    it('rejects an empty or over-long key', () => {
        expect(parseCatalog({ '': 'x' }).ok).toBe(false);
        expect(parseCatalog({ ['k'.repeat(201)]: 'x' }).ok).toBe(false);
    });

    it('rejects a value over the per-value bound', () => {
        const res = parseCatalog({ 'a.b': 'x'.repeat(MAX_VALUE_LEN + 1) });
        expect(res.ok).toBe(false);
        if (!res.ok) expect(res.reason).toContain(`${MAX_VALUE_LEN}`);
    });

    it('rejects a catalog over the key bound', () => {
        const big: Record<string, string> = {};
        for (let i = 0; i <= MAX_CATALOG_KEYS; i++) big[`k${i}`] = 'v';
        const res = parseCatalog(big);
        expect(res.ok).toBe(false);
        if (!res.ok) expect(res.reason).toContain(`${MAX_CATALOG_KEYS}`);
    });

    it('reports a syntax error as a reason instead of throwing', () => {
        const res = parseCatalogJSON('{"a": ');
        expect(res.ok).toBe(false);
        if (!res.ok) expect(res.reason).toMatch(/not valid JSON/);
    });

    it('rejects a body over the byte bound without parsing it', () => {
        const res = parseCatalogJSON('"' + 'x'.repeat(1_100_000) + '"');
        expect(res.ok).toBe(false);
        if (!res.ok) expect(res.reason).toMatch(/bytes/);
    });
});
