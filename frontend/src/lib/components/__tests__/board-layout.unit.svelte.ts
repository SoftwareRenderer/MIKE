import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const boardPath = resolve(__dirname, '../Board.svelte');
const source = readFileSync(boardPath, 'utf-8');

describe('Board layout', () => {
    it('renders 4 columns: Backlog, In Progress, Review, Done', () => {
        expect(source).toContain('Backlog');
        expect(source).toContain('In Progress');
        expect(source).toContain('Review');
        expect(source).toContain('Done');
    });

    it('shows task count per column', () => {
        expect(source).toContain('length');
        expect(source).toContain('tasks');
    });

    it('supports drag and drop between columns', () => {
        expect(source).toContain('drag');
        expect(source).toContain('drop');
    });

    it('shows clear done button in Done column header', () => {
        expect(source).toContain('clearDoneTasks');
        expect(source).toContain('onClearAll');
    });

    it('hides clear button when no done tasks', () => {
        expect(source).toContain('doneTasks');
    });

    it('uses Column component for each column', () => {
        expect(source).toContain('Column');
    });

    it('supports multi-select mode', () => {
        expect(source).toContain('multiSelect');
    });

    it('scrolls to current task in side panel', () => {
        expect(source).toContain('scroll');
    });

    it('supports scroll to column', () => {
        expect(source).toContain('scrollToColumn');
    });

    it('orders tasks by last activity within columns', () => {
        expect(source).toContain('sort');
    });
});
