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
        const idx = source.indexOf('btn-primary');
        const block = source.slice(idx, source.indexOf('</button>', idx));
        expect(block).toContain('{#if submitHintLabel && !hintsHidden}<kbd class="key-hint">{submitHintLabel}</kbd>{/if}');
        // The label must appear once — regression: duplicated button text
        const labels = block.match(/loading \? 'Creating\.\.\.' : 'Create Task'/g) || [];
        expect(labels.length).toBe(1);
    });
});
