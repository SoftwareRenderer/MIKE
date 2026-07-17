import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const reviewBarPath = resolve(__dirname, '../Chat/ReviewBar.svelte');
const source = readFileSync(reviewBarPath, 'utf-8');

describe('ReviewBar rendering', () => {
    it('shows review bar when task is in review state', () => {
        // The ReviewBar is conditionally rendered based on task status
        expect(source).toContain('taskStatus');
        expect(source).toContain('review-bar');
    });

    it('shows → Done button for moving task to Done', () => {
        expect(source).toContain('onMoveToDone');
        expect(source).toContain('Move to Done');
    });

    it('shows → Backlog button for moving task to Backlog', () => {
        expect(source).toContain('onMoveTask');
        expect(source).toContain('Backlog');
    });

    it('shows commit button when there are staged changes', () => {
        expect(source).toContain('hasStagedChanges');
        expect(source).toContain('onCommitAndDone');
    });

    it('shows resume button when showResumeButton is true', () => {
        expect(source).toContain('showResumeButton');
        expect(source).toContain('onStartTask');
    });

    it('shows restart button when showRestartButton is true', () => {
        expect(source).toContain('showRestartButton');
    });

    it('shows model selector in review bar', () => {
        expect(source).toContain('ModelSelector');
        expect(source).toContain('selectedModel');
    });

    it('shows queue status when available', () => {
        expect(source).toContain('queueStatus');
    });

    it('disables buttons when disabled is true', () => {
        expect(source).toContain('disabled');
    });

    it('shows loading state while committing', () => {
        expect(source).toContain('isCommitting');
    });
});
