import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { EditorState } from '@codemirror/state';
import { json } from '@codemirror/lang-json';
import { keepViewportParsed } from '../keep-viewport-parsed';

// Mock parse functions; keep the real `language` facet for the guard test.
vi.mock('@codemirror/language', async (importOriginal) => {
    const actual = await importOriginal<typeof import('@codemirror/language')>();
    return {
        ...actual,
        forceParsing: vi.fn(() => true),
        syntaxTreeAvailable: vi.fn(() => true),
    };
});

import { forceParsing, syntaxTreeAvailable } from '@codemirror/language';
const forceParsingMock = vi.mocked(forceParsing);
const syntaxTreeAvailableMock = vi.mocked(syntaxTreeAvailable);

// Captured requestAnimationFrame callbacks (we control frame timing).
let rafCallbacks: FrameRequestCallback[];
let cancelRaf: ReturnType<typeof vi.fn>;

function makeView(doc: string, withLang: boolean): any {
    // Any real grammar will do: the extension under test only asks whether a language is loaded.
    const extensions = withLang ? [json()] : [];
    const state = EditorState.create({ doc, extensions });
    return {
        state,
        viewport: { from: 0, to: Math.min(200, doc.length) },
        dom: { isConnected: true },
        dispatch: vi.fn(),
    };
}

/** Create the plugin instance exactly like EditorView would. */
function createPlugin(view: any, timeout = 16, limit = 1_000_000) {
    const plugin = keepViewportParsed(timeout, limit);
    return { plugin, instance: (plugin as any).create(view) };
}

function runFrames(count = 100) {
    // Run captured rAF callbacks in order, following chains until exhausted.
    for (let i = 0; i < count && rafCallbacks.length > 0; i++) {
        const cb = rafCallbacks.shift()!;
        cb(performance.now());
    }
}

beforeEach(() => {
    rafCallbacks = [];
    vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => {
        rafCallbacks.push(cb);
        return rafCallbacks.length;
    });
    cancelRaf = vi.fn();
    vi.stubGlobal('cancelAnimationFrame', cancelRaf);
    forceParsingMock.mockClear().mockReturnValue(true);
    syntaxTreeAvailableMock.mockClear().mockReturnValue(true);
});

afterEach(() => {
    vi.unstubAllGlobals();
});

describe('keepViewportParsed', () => {
    it('schedules a viewport force-parse on viewport change for large files', () => {
        const view = makeView('x'.repeat(2_000_000), true);
        const { instance } = createPlugin(view, 16, 1_000_000);
        expect(rafCallbacks.length).toBe(0); // large file: no constructor drive

        instance.update({ viewportChanged: true, docChanged: false, state: view.state, view });
        expect(rafCallbacks.length).toBe(1);
        runFrames();
        expect(forceParsingMock).toHaveBeenCalledWith(view, view.viewport.to, 16);
    });

    it('ignores viewport changes for plain-text files (no language)', () => {
        const view = makeView('x'.repeat(2_000_000), false);
        const { instance } = createPlugin(view);
        instance.update({ viewportChanged: true, docChanged: false, state: view.state, view });
        expect(rafCallbacks.length).toBe(0);
        expect(forceParsingMock).not.toHaveBeenCalled();
    });

    it('drives a full-document parse after creation for files within the limit', () => {
        const view = makeView('x'.repeat(200_000), true);
        syntaxTreeAvailableMock.mockReturnValue(false);
        const { instance } = createPlugin(view, 16, 1_000_000);

        // Constructor scheduled the first drive slice.
        expect(rafCallbacks.length).toBe(1);
        forceParsingMock.mockReturnValue(false); // slice times out
        runFrames(1);
        expect(forceParsingMock).toHaveBeenCalledWith(view, 200_000, 16);
        // Not done → chained another slice.
        expect(rafCallbacks.length).toBe(1);

        // Second slice completes the doc → drive stops.
        forceParsingMock.mockReturnValue(true);
        runFrames(1);
        expect(rafCallbacks.length).toBe(0);
        expect(instance.driving).toBe(false);
    });

    it('does not drive a full parse when the document is already fully parsed', () => {
        const view = makeView('x'.repeat(200_000), true);
        syntaxTreeAvailableMock.mockReturnValue(true);
        createPlugin(view);
        expect(rafCallbacks.length).toBe(0);
        expect(forceParsingMock).not.toHaveBeenCalled();
    });

    it('does not drive a full parse for files above the limit', () => {
        const view = makeView('x'.repeat(2_000_000), true);
        syntaxTreeAvailableMock.mockReturnValue(false);
        createPlugin(view, 16, 1_000_000);
        expect(rafCallbacks.length).toBe(0);
        expect(forceParsingMock).not.toHaveBeenCalled();
    });

    it('resumes the full parse when the viewport changes while driving', () => {
        const view = makeView('x'.repeat(200_000), true);
        syntaxTreeAvailableMock.mockReturnValue(false);
        const { instance } = createPlugin(view);
        expect(rafCallbacks.length).toBe(1); // constructor drive

        instance.update({ viewportChanged: true, docChanged: false, state: view.state, view });
        // Already driving → no duplicate scheduling.
        expect(rafCallbacks.length).toBe(1);
        runFrames();
        expect(forceParsingMock).toHaveBeenCalled();
    });

    it('resumes the full parse after a document change', () => {
        const view = makeView('x'.repeat(200_000), true);
        syntaxTreeAvailableMock.mockReturnValue(false);
        const { instance } = createPlugin(view);
        runFrames(); // drive runs to completion (syntaxTreeAvailable true)
        expect(instance.driving).toBe(false);

        // Editing invalidates the tree → drive resumes on docChanged.
        syntaxTreeAvailableMock.mockReturnValue(false);
        instance.update({ viewportChanged: false, docChanged: true, state: view.state, view });
        expect(rafCallbacks.length).toBe(1);
    });

    it('destroy cancels a pending frame', () => {
        const view = makeView('x'.repeat(200_000), true);
        syntaxTreeAvailableMock.mockReturnValue(false);
        const { instance } = createPlugin(view);
        expect(rafCallbacks.length).toBe(1);
        instance.destroy();
        expect(cancelRaf).toHaveBeenCalled();
    });

    it('stops chaining after the defensive iteration cap', () => {
        const view = makeView('x'.repeat(200_000), true);
        syntaxTreeAvailableMock.mockReturnValue(false);
        forceParsingMock.mockReturnValue(false);
        const { instance } = createPlugin(view, 16, 1_000_000);
        runFrames(500);
        // 101 slices (initial + 100 cap) max, then stops.
        expect(forceParsingMock.mock.calls.length).toBeLessThanOrEqual(101);
        expect(instance.driving).toBe(false);
        expect(rafCallbacks.length).toBe(0);
    });
});
