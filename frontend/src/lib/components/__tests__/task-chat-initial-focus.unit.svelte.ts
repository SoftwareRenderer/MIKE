import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const taskChatSource = readFileSync(resolve(__dirname, '../Chat/TaskChat.svelte'), 'utf-8');

describe('TaskChat chat input initial focus', () => {
    it('focuses the chat input on open, after history is loaded', () => {
        // The initial focus must run inside the onMount init chain, AFTER
        // loadHistory() (i.e. after `suppressEvents = false`) so the restart-state
        // condition (which needs loaded entries) is already resolved when the
        // focus is attempted.
        const focusAfterHistory = taskChatSource.match(
            /suppressEvents = false;[\s\S]*?chatFormComp\?\.focus\(\)/
        );
        expect(focusAfterHistory, 'expected chatFormComp?.focus() after history load in onMount init').not.toBeNull();
    });

    it('guards the initial focus against a destroyed component', () => {
        // If the user navigates away during async init, the focus attempt must be
        // skipped (the component is unmounted by then).
        const guarded = taskChatSource.match(/if \(!destroyed &&/);
        expect(guarded, 'expected initial focus to be guarded on `!destroyed`').not.toBeNull();
    });

    it('does not auto-focus the chat input on mobile', () => {
        // On mobile, selecting a task should not pop the virtual keyboard.
        // The initial focus in the onMount init chain must be gated on a
        // mobile check (via useIsMobile).
        const mobileGuard = taskChatSource.match(
            /if \(!destroyed && !isMobile\) chatFormComp\?\.focus\(\)/
        );
        expect(mobileGuard, 'expected initial focus to be skipped when isMobile').not.toBeNull();
    });

    it('uses the shared useIsMobile composable for the mobile check', () => {
        expect(taskChatSource, 'expected useIsMobile import').toContain("from '$lib/composables/use-is-mobile.svelte'");
        expect(taskChatSource, 'expected useIsMobile() call').toMatch(/useIsMobile\(\)/);
    });
});
