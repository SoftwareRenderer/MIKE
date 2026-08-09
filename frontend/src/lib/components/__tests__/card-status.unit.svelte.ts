import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const cardPath = resolve(__dirname, '../Card.svelte');
const cardDetailPath = resolve(__dirname, '../CardDetail.svelte');
const taskChatPath = resolve(__dirname, '../Chat/TaskChat.svelte');
const source = readFileSync(cardPath, 'utf-8');
const cardDetailSource = readFileSync(cardDetailPath, 'utf-8');
const taskChatSource = readFileSync(taskChatPath, 'utf-8');

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

    it('uses $state + $effect to track streamingIds for reactive status badge (Card.svelte)', () => {
        // get() on a Svelte store inside $derived.by does NOT establish a dependency.
        // The fix syncs the store to a $state variable via $effect.
        expect(source).toContain('streamingIds = $state');
        expect(source).toContain('streamingIdsContext.subscribe');
        // Status badge should read from the $state variable, not get()
        expect(source).toContain('streamingIds.has(task.id)');
    });

    it('uses $state + $effect to track streamingIds for reactive status badge (CardDetail.svelte)', () => {
        expect(cardDetailSource).toContain('streamingIds = $state');
        expect(cardDetailSource).toContain('streamingIdsContext.subscribe');
        expect(cardDetailSource).toContain('streamingIds.has(task.id)');
    });

    it('shows task as draggable', () => {
        expect(source).toContain('draggable');
    });

    it('clears streamingIds in TaskChat onDestroy to prevent persistent "Running" badge', () => {
        // When TaskChat unmounts (detail closed), it must call markNotStreaming()
        // so the global streamingIds store doesn't retain a stale entry.
        // Without this, the card's status badge shows "Running" forever even
        // after the task completes (execution_state → idle) because streamingIds
        // still has the task ID.
        // Verify markNotStreaming is called within the onDestroy callback.
        const onDestroyMatch = taskChatSource.match(/onDestroy\(\(\)\s*=>\s*\{[^}]*\}/s);
        expect(onDestroyMatch).not.toBeNull();
        expect(onDestroyMatch![0]).toContain('markNotStreaming');
    });
});
