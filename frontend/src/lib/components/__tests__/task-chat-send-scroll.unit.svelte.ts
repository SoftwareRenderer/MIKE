import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const taskChatSource = readFileSync(resolve(__dirname, '../Chat/TaskChat.svelte'), 'utf-8');

/** Extract the body of a top-level async function from the source. */
function fnBody(name: string): string {
    const start = taskChatSource.indexOf(`async function ${name}(`);
    expect(start, `expected function ${name} to exist`).toBeGreaterThan(-1);
    const end = taskChatSource.indexOf('    async function ', start + 1);
    return taskChatSource.slice(start, end === -1 ? undefined : end);
}

describe('TaskChat — scroll on user-initiated send', () => {
    it('forces the viewport to the bottom when handleSend fires', () => {
        // A user who sends a message expects to see it — even if they were
        // scrolled up to read — so handleSend must use a FORCED scroll (true).
        expect(fnBody('handleSend')).toContain('scroll(true)');
    });

    it('forces the scroll immediately after the optimistic entry is added', () => {
        // The forced scroll must run right after the optimistic user entry is
        // appended (before the network await) so the sent message appears in view
        // without waiting for the send round-trip.
        const start = taskChatSource.indexOf("stream.state.entries = [...stream.state.entries, { id: entryId, role: 'user'");
        expect(start, 'expected the optimistic entry append in handleSend').toBeGreaterThan(-1);
        const block = taskChatSource.slice(start, start + 700);
        const scrollIdx = block.indexOf('scroll(true)');
        expect(scrollIdx, 'expected a forced scroll after the optimistic entry').toBeGreaterThan(-1);
    });

    it('forces the scroll on edit-and-send', () => {
        expect(fnBody('handleEditSend')).toContain('scroll(true)');
    });

    it('forces the scroll on regenerate', () => {
        expect(fnBody('handleRegenerate')).toContain('scroll(true)');
    });

    it('does NOT force the scroll on initial history load', () => {
        // Restoring history should not yank the viewport — keep the non-forced
        // scroll so a user who was scrolled up stays put.
        expect(fnBody('loadHistory')).not.toContain('scroll(true)');
    });
});
