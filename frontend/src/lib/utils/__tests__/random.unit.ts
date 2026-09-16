/**
 * The identifiers the client mints for the server to store.
 *
 * A slip back to `crypto.randomUUID`, or to the clock plus `Math.random` as `generateId` used to
 * be, is visible only on an origin without `randomUUID` — which `withoutRandomUUID` fakes.
 */
import { describe, it, expect, vi, afterEach } from 'vitest';
import { randomHex, randomUUIDv4 } from '../random';
import { generateId } from '$lib/api/helpers';

const UUID_V4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

/**
 * Run `run` with `crypto.randomUUID` unfindable, as it is on a non-secure origin. `delete` is a
 * no-op here because jsdom defines it on `Crypto.prototype`; an own property shadows that.
 */
function withoutRandomUUID<T>(run: () => T): T {
    const holder = crypto as unknown as Record<string, unknown>;
    const had = Object.getOwnPropertyDescriptor(holder, 'randomUUID');
    Object.defineProperty(holder, 'randomUUID', { value: undefined, configurable: true, writable: true });
    try {
        return run();
    } finally {
        if (had) Object.defineProperty(holder, 'randomUUID', had);
        else delete holder.randomUUID;
    }
}

describe('randomUUIDv4', () => {
    afterEach(() => vi.restoreAllMocks());

    it('is a v4 UUID with the RFC 4122 variant', () => {
        for (let i = 0; i < 200; i++) expect(randomUUIDv4()).toMatch(UUID_V4);
    });

    it('does not repeat', () => {
        const seen = new Set(Array.from({ length: 5000 }, () => randomUUIDv4()));
        expect(seen.size).toBe(5000);
    });

    it('needs only getRandomValues', () => {
        expect(withoutRandomUUID(() => randomUUIDv4())).toMatch(UUID_V4);
    });

    it('asks the CSPRNG and not Math.random', () => {
        const spy = vi.spyOn(Math, 'random');
        randomUUIDv4();
        randomHex(16);
        expect(spy).not.toHaveBeenCalled();
    });
});

describe('randomHex', () => {
    it('is the requested length of lowercase hex', () => {
        expect(randomHex(16)).toMatch(/^[0-9a-f]{32}$/);
        expect(randomHex(1)).toMatch(/^[0-9a-f]{2}$/);
    });
});

describe('generateId', () => {
    afterEach(() => vi.restoreAllMocks());

    it('is a v4 UUID — the shape the server stores as a row identity', () => {
        expect(generateId()).toMatch(UUID_V4);
    });

    it('is unpredictable without Math.random', () => {
        const spy = vi.spyOn(Math, 'random');
        const ids = new Set(Array.from({ length: 100 }, () => generateId()));
        expect(spy).not.toHaveBeenCalled();
        expect(ids.size).toBe(100);
    });

    it('is the same shape on a plain-HTTP origin, where randomUUID is absent', () => {
        const spy = vi.spyOn(Math, 'random');
        const id = withoutRandomUUID(() => generateId());
        expect(id).toMatch(UUID_V4);
        expect(spy).not.toHaveBeenCalled();
    });
});
