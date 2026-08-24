import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const source = readFileSync(resolve(__dirname, '../CreateTaskModal.svelte'), 'utf-8');

describe('Create Task submit shortcut wiring', () => {
    it('registers a window keydown listener for the lifetime of the modal', () => {
        expect(source).toContain("window.addEventListener('keydown'");
        expect(source).toContain('window.removeEventListener');
    });

    it('submits on the configurable binding: match -> preventDefault -> handleSubmit', () => {
        const idxMatch = source.indexOf('matchesCreateTaskSubmitKey');
        expect(idxMatch).toBeGreaterThan(-1);
        const idxPrevent = source.indexOf('e.preventDefault()', idxMatch);
        // The shortcut triggers the single submit path — in the line-task
        // variant that creates + opens the floating task view.
        const idxSubmit = source.indexOf('handleSubmit()', idxMatch);
        expect(idxPrevent).toBeGreaterThan(idxMatch);
        expect(idxSubmit).toBeGreaterThan(idxPrevent);
    });

    it('reuses one canSubmit guard for the shortcut and the Create button', () => {
        expect(source).toContain('const canSubmit = $derived(');
        expect(source).toContain('disabled={!canSubmit}');
    });

    it('shows the platform-appropriate shortcut in the button title', () => {
        expect(source).toContain('effectiveCreateTaskSubmitKey');
    });

    it('shows the shortcut as a keycap on the Create button (exactly one label)', () => {
        // The footer has a single Create Task button shared by both variants
        // (the line-task variant adds an auto-start option above it).
        const idx = source.indexOf("loading ? 'Creating...' : 'Create Task'");
        expect(idx, 'Create button label not found').toBeGreaterThan(-1);
        const start = source.lastIndexOf('btn-primary', idx);
        const block = source.slice(start, source.indexOf('</button>', idx));
        expect(block).toContain('{#if submitHintLabel && !hintsHidden}<kbd class="key-hint">{submitHintLabel}</kbd>{/if}');
        // The label must appear once in the whole file — regression: duplicated
        // button text (a second variant button with the same label).
        const labels = source.match(/loading \? 'Creating\.\.\.' : 'Create Task'/g) || [];
        expect(labels.length).toBe(1);
    });
});
