import { describe, it, expect } from 'vitest';
import { SaveController, SaveRequestGuard } from '$lib/utils/save-state';

describe('SaveController.begin', () => {
    it('starts a save when there is something to write', () => {
        const c = new SaveController();
        expect(c.begin(true)).toBe(true);
        expect(c.busy).toBe(true);
    });

    it('refuses a save with nothing to write, and stays idle', () => {
        const c = new SaveController();
        expect(c.begin(false)).toBe(false);
        expect(c.busy).toBe(false);
    });

    it('refuses a second save while one is in flight', () => {
        const c = new SaveController();
        expect(c.begin(true)).toBe(true);
        expect(c.begin(true)).toBe(false);
    });

    it('refuses every further request while in flight, not just the first', () => {
        const c = new SaveController();
        c.begin(true);
        expect(c.begin(true)).toBe(false);
        expect(c.begin(true)).toBe(false);
        expect(c.settle(true)).toBe(true);
    });

    it('does not start a second request when an in-flight refusal arrives with nothing to write', () => {
        const c = new SaveController();
        c.begin(true);
        expect(c.begin(false)).toBe(false);
        // The refusal was noted, but the buffer is clean once the first save lands.
        expect(c.settle(false)).toBe(false);
    });
});

describe('SaveController.settle', () => {
    it('asks for a re-save when a request arrived during the save and edits are still unsaved', () => {
        const c = new SaveController();
        c.begin(true);
        c.begin(true); // typed while saving, pressed save again
        expect(c.settle(true)).toBe(true);
    });

    it('does not re-save when nothing is left dirty', () => {
        const c = new SaveController();
        c.begin(true);
        c.begin(true);
        expect(c.settle(false)).toBe(false);
    });

    it('leaves a lone save alone — no queued request means no second save', () => {
        const c = new SaveController();
        c.begin(true);
        expect(c.settle(true)).toBe(false);
    });

    it('frees the editor so the next save can start', () => {
        const c = new SaveController();
        c.begin(true);
        c.settle(false);
        expect(c.busy).toBe(false);
        expect(c.begin(true)).toBe(true);
    });

    it('consumes the queued request — settling twice does not keep re-saving', () => {
        const c = new SaveController();
        c.begin(true);
        c.begin(true);
        expect(c.settle(true)).toBe(true);
        // The follow-up save itself had no new request behind it.
        c.begin(true);
        expect(c.settle(true)).toBe(false);
    });
});

describe('SaveController.reset', () => {
    it('drops an in-flight save and any queued request', () => {
        const c = new SaveController();
        c.begin(true);
        c.begin(true);
        c.reset();
        expect(c.busy).toBe(false);
        expect(c.settle(true)).toBe(false);
    });
});

describe('SaveRequestGuard', () => {
    it('lets the first request out and blocks a second', () => {
        const g = new SaveRequestGuard();
        expect(g.request('/a.go')).toBe(true);
        expect(g.busy).toBe(true);
        expect(g.request('/a.go')).toBe(false);
    });

    it('refuses a request with no file to save', () => {
        const g = new SaveRequestGuard();
        expect(g.request('')).toBe(false);
        expect(g.busy).toBe(false);
    });

    it('frees the button when the editor answers for the file it was asked about', () => {
        const g = new SaveRequestGuard();
        g.request('/a.go');
        expect(g.settled('/a.go')).toBe(true);
        expect(g.busy).toBe(false);
    });

    it('ignores an answer for a different file — the other explorer owns that one', () => {
        const g = new SaveRequestGuard();
        g.request('/a.go');
        expect(g.settled('/other.go')).toBe(false);
        expect(g.busy).toBe(true);
    });

    it('ignores an answer that names no file', () => {
        const g = new SaveRequestGuard();
        g.request('/a.go');
        expect(g.settled(undefined)).toBe(false);
        expect(g.busy).toBe(true);
    });

    it('accepts a new request once the previous one settled', () => {
        const g = new SaveRequestGuard();
        g.request('/a.go');
        g.settled('/a.go');
        expect(g.request('/b.go')).toBe(true);
    });

    it('frees the button when nothing answered and the backstop fired', () => {
        const g = new SaveRequestGuard();
        g.request('/a.go');
        g.expire();
        expect(g.busy).toBe(false);
        // And the next click works, rather than staying wedged.
        expect(g.request('/a.go')).toBe(true);
    });

    it('accepts a late answer arriving after the backstop — nothing is being waited on', () => {
        const g = new SaveRequestGuard();
        g.request('/a.go');
        g.expire();
        // Stale but harmless: at worst it frees a request early, and a duplicate
        // is queued by the editor rather than lost.
        expect(g.settled('/a.go')).toBe(true);
        expect(g.busy).toBe(false);
    });
});
