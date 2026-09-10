import { describe, it, expect } from 'vitest';
import {
    revealFileTab,
    mergeRestoredTabs,
    toPersistedTabs,
    fromPersistedTabs,
    type OpenFileTab,
} from '$lib/utils/editor-tabs';

const nonePinned = () => false;
const allPinned = () => true;

/** A tab list as the explorers hold it after opening `main.go` at line 10. */
function openAt(path: string, line: number, reveal = 1): OpenFileTab[] {
    return [{ path, targetLine: line, reveal }];
}

describe('revealFileTab', () => {
    it('a second result in an already-open file lands its line on that tab', () => {
        // The reported bug: "Find in files" opens the file on the first result,
        // and the second result in the same file scrolled nowhere, because the
        // already-open branch switched tabs without recording the line.
        const first = revealFileTab([], 'main.go', 10, nonePinned);
        const second = revealFileTab(first, 'main.go', 42, nonePinned);

        expect(second).toHaveLength(1);
        expect(second[0]).toEqual({ path: 'main.go', targetLine: 42, reveal: 2 });
    });

    it('the same line asked for twice is a second reveal request', () => {
        // Clicking a match, scrolling away, then clicking the same match again
        // must scroll back — which needs a value that changed.
        const first = revealFileTab([], 'main.go', 42, nonePinned);
        const again = revealFileTab(first, 'main.go', 42, nonePinned);

        expect(again[0].reveal).toBe(2);
    });

    it('a plain open of an open file keeps its place', () => {
        // A file-browser click names no line; it must not yank the cursor.
        const opened = revealFileTab([], 'main.go', 42, nonePinned);
        const clicked = revealFileTab(opened, 'main.go', 0, nonePinned);

        expect(clicked).toBe(opened);
    });

    it('opening a new file records its line and one reveal request', () => {
        expect(revealFileTab([], 'main.go', 7, nonePinned)).toEqual([
            { path: 'main.go', targetLine: 7, reveal: 1 },
        ]);
        expect(revealFileTab([], 'main.go', 0, nonePinned)).toEqual([
            { path: 'main.go', targetLine: 0, reveal: 0 },
        ]);
    });

    it('a new file replaces the unpinned preview tab and appends when all tabs are pinned', () => {
        const preview = openAt('a.go', 1);

        expect(revealFileTab(preview, 'b.go', 2, nonePinned).map(f => f.path)).toEqual(['b.go']);
        expect(revealFileTab(preview, 'b.go', 2, allPinned).map(f => f.path)).toEqual(['a.go', 'b.go']);
    });

    it('the request leaves the tab list it was given untouched', () => {
        const before = openAt('main.go', 10);

        revealFileTab(before, 'main.go', 42, nonePinned);
        revealFileTab(before, 'other.go', 3, nonePinned);

        expect(before).toEqual([{ path: 'main.go', targetLine: 10, reveal: 1 }]);
    });
});

describe('tab persistence', () => {
    it('a restored tab reveals nothing until something asks it to', () => {
        // Otherwise every reload replays the click that opened the file.
        const persisted = toPersistedTabs(revealFileTab([], 'main.go', 42, nonePinned));

        expect(persisted).toEqual([{ path: 'main.go', targetLine: 42 }]);
        expect(fromPersistedTabs(persisted)).toEqual([
            { path: 'main.go', targetLine: 42, reveal: 0 },
        ]);
    });

    it('a persisted tab with no line restores as asking for no line', () => {
        expect(fromPersistedTabs([{ path: 'main.go', targetLine: 0 }])).toEqual([
            { path: 'main.go', targetLine: 0, reveal: 0 },
        ]);
    });
});

describe('mergeRestoredTabs', () => {
    it('keeps the file whose jump opened the explorer, restored around it', () => {
        // The saved tabs come back from IndexedDB asynchronously, and a jump (a tool
        // card's file, a definition out of a diff) can be what opens the explorer — so
        // the restore answers after the file was opened. Overwriting the list there
        // silently loses the click.
        const arrived = revealFileTab([], 'src/main.go', 0, nonePinned);
        const restored = fromPersistedTabs([{ path: 'README.md', targetLine: 0 }]);

        expect(mergeRestoredTabs(arrived, restored).map(f => f.path))
            .toEqual(['README.md', 'src/main.go']);
    });

    it('does not open twice a file both the saved session and the jump name', () => {
        const arrived = revealFileTab([], 'src/main.go', 0, nonePinned);
        const restored = fromPersistedTabs([{ path: 'src/main.go', targetLine: 0 }]);

        const merged = mergeRestoredTabs(arrived, restored);
        expect(merged).toEqual([{ path: 'src/main.go', targetLine: 0, reveal: 0 }]);
    });

    it('restores the saved session untouched when nothing arrived meanwhile', () => {
        const restored = fromPersistedTabs([
            { path: 'a.go', targetLine: 0 },
            { path: 'b.go', targetLine: 12 },
        ]);

        expect(mergeRestoredTabs([], restored)).toEqual(restored);
    });
});
