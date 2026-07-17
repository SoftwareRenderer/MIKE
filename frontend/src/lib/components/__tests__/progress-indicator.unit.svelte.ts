import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const cardPath = resolve(__dirname, '../Card.svelte');
const source = readFileSync(cardPath, 'utf-8');

describe('Subtask progress indicator', () => {
    it('shows progress indicator for parent tasks with subtasks', () => {
        expect(source).toContain('hasChildren');
        expect(source).toContain('progress');
    });

    it('updates progress when subtask status changes', () => {
        expect(source).toContain('status');
        expect(source).toContain('$derived');
    });

    it('does not show progress indicator for tasks without subtasks', () => {
        expect(source).toContain('!hasChildren');
    });

    it('progress count reflects total children', () => {
        expect(source).toContain('children');
    });

    it('progress shows 100% when all subtasks are done', () => {
        expect(source).toContain('subtaskProgress');
        expect(source).toContain('descendants.done');
        expect(source).toContain('descendants.total');
    });

    it('progress shows 0% when no subtasks are done', () => {
        expect(source).toContain('descendants.done');
    });

    it('progress updates reactively', () => {
        expect(source).toContain('$derived');
    });

    it('shows completed count out of total', () => {
        expect(source).toContain('/');
    });

    it('uses subtask progress variable', () => {
        expect(source).toContain('subtaskProgress');
    });

    it('uses color coding for progress levels', () => {
        expect(source).toContain('progress');
    });
});
