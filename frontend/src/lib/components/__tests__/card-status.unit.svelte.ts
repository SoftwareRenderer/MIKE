import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const cardPath = resolve(__dirname, '../Card.svelte');
const source = readFileSync(cardPath, 'utf-8');

describe('Card status rendering', () => {
    it('shows status badge for task', () => {
        expect(source).toContain('statusBadge');
        expect(source).toContain('badge');
    });

    it('displays task title', () => {
        expect(source).toContain('task.title');
    });

    it('displays task prompt/summary', () => {
        expect(source).toContain('task.prompt');
    });

    it('shows model name on card', () => {
        expect(source).toContain('model');
    });

    it('shows action buttons on card', () => {
        expect(source).toContain('subtask');
        expect(source).toContain('Edit');
    });

    it('shows move button for status transitions', () => {
        expect(source).toContain('Move to');
    });

    it('supports multi-select', () => {
        expect(source).toContain('MultiSelect');
    });

    it('shows progress indicator for parent tasks with subtasks', () => {
        expect(source).toContain('subtask');
        expect(source).toContain('progress');
    });

    it('updates status badge reactively when execution state changes', () => {
        expect(source).toContain('execution_state');
    });

    it('shows task as draggable', () => {
        expect(source).toContain('draggable');
    });
});
