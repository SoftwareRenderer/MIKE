import { test, expect } from '@playwright/test';
import {
  getProjectId, createTaskViaApi, navigateToTask, ensureTaskStarted, isolatedProject, gotoProject,
  sendTurn, messageRows, waitForSendButton,
} from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();

// What a reader does to a message they typed: copy it, and delete it with everything after it. One
// conversation carries all three decisions, because each one is about a row the send before it produced.
// Ids, never counts: `/messages` answers a bounded window (see `messageRows` in helpers.ts), so a count read
// at two moments cannot show growth, and this test is about a row being there, absent, or there twice.

/** The delete confirmation, told apart from every other dialog on a task view by what it says. */
function deleteDialog(page: import('@playwright/test').Page) {
  return page.locator('[role="dialog"]').filter({ hasText: 'This deletes this message and every message after it' });
}

test.describe.serial('Chat Message Actions', () => {
  let projectId: string;
  let taskId: string;

  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.card', { timeout: 10000 });

    projectId = await getProjectId(page);
    // Pinned to `text`: the task's own turn must answer and end, or every wait below queues behind it.
    taskId = await createTaskViaApi(page, projectId, `message-actions-${Date.now()} llm-fixture:text`);
    await navigateToTask(page, taskId, projectId);
    await ensureTaskStarted(page, projectId, taskId);
    await waitForSendButton(page);
  });

  test('a message can be copied, only a later one can be deleted, and deleting one leaves nothing behind', async ({ page }) => {
    test.setTimeout(150000);
    const assistants = async () =>
      (await messageRows(page, projectId, taskId))
        .filter((m) => m.role === 'assistant').map((m) => m.id);

    const msg1 = `first message ${Date.now()}`;
    await sendTurn(page, projectId, taskId, msg1);
    const afterFirst = await assistants();
    // A turn can append several assistant entries (the mock LLM answers in more than
    // one message), so this test works with ids, never with "one turn == one row".
    expect(afterFirst.length, 'the first turn wrote no assistant row').toBeGreaterThanOrEqual(1);
    const firstText = (await page.locator('.chat-message.assistant').first().textContent())?.trim();
    expect(firstText).not.toBe('');

    const msg2 = `second message ${Date.now()}`;
    await sendTurn(page, projectId, taskId, msg2);
    const afterSecond = await assistants();
    // The second turn's answer is an entry this conversation never held before, and nothing is stored twice.
    expect(afterSecond.filter(id => !afterFirst.includes(id)), 'the second turn added no assistant entry')
      .not.toHaveLength(0);
    expect(new Set(afterSecond).size, 'an assistant entry is stored twice').toBe(afterSecond.length);

    // ── Copying a message answers with the check, and then stops answering.
    const secondRow = page.locator('.chat-message.user', { hasText: msg2 });
    const copyBtn = secondRow.locator('[aria-label="Copy message"]');
    await expect(copyBtn).toBeVisible();
    await copyBtn.click();
    await expect(copyBtn).toHaveClass(/copied/);
    // Wait for the "copied" class to be removed (visual feedback clears after ~2s)
    await expect(copyBtn).not.toHaveClass(/copied/, { timeout: 5000 });

    // ── Only a message that is not the conversation's first offers deletion. The task's own prompt is
    // the first user row, so it holds no delete button at all — a node the code never creates, which is
    // what `toHaveCount(0)` says and `not.toBeVisible()` would hide.
    const firstRow = page.locator('.chat-message.user').first();
    await expect(firstRow).toContainText(`message-actions-`);
    await expect(firstRow.locator('[aria-label="Delete message and all following messages"]')).toHaveCount(0);
    await expect(page.locator('.chat-message.user', { hasText: msg1 })
      .locator('[aria-label="Delete message and all following messages"]')).toBeVisible();
    const deleteBtn = secondRow.locator('[aria-label="Delete message and all following messages"]');
    await expect(deleteBtn).toBeVisible();

    // ── Deleting the last user row takes its answer with it, from the store and from the DOM, with no
    // duplicate left behind and the first answer untouched.
    const rowsBeforeDelete = (await messageRows(page, projectId, taskId)).map(r => r.id);
    await deleteBtn.click();
    const dialog = deleteDialog(page);
    await expect(dialog).toBeVisible({ timeout: 5000 });
    await dialog.locator('.btn-confirm').click();
    await expect(dialog).toBeHidden({ timeout: 5000 });

    await expect(async () => {
      expect(new Set(await assistants())).toEqual(new Set(afterFirst));
    }).toPass({ timeout: 15000 });

    // The screen keeps the rows that are stored and loses the ones that were deleted. Asked by identity:
    // the deleted ids are the rows this read no longer answers, and the transcript draws entries for more
    // than rows (a tool card names itself, not a row), so the claim is made in both directions rather than
    // as an equality the transcript was never meant to satisfy.
    const drawnIds = () => page.evaluate(() => Array.from(
      document.querySelectorAll<HTMLElement>('.chat-entry[data-entry-id]'), node => node.dataset.entryId || '',
    ));
    const rowsAfter = await messageRows(page, projectId, taskId);
    const rowIdsAfter = rowsAfter.map(r => r.id);
    const deleted = rowsBeforeDelete.filter(id => !rowIdsAfter.includes(id));
    // Only a row the reader typed is drawn under its own id: a live-streamed answer keeps the id the
    // stream gave it until a read replaces it, and a tool card names itself. Those are asked below by
    // what they say, not by an id they were never given.
    const surviving = rowsAfter.filter(r => r.role === 'user').map(r => r.id);
    expect(deleted.length, 'nothing was deleted, so this run deleted nothing').toBeGreaterThan(0);

    // The reload the delete triggers is what takes those entries off screen, so the absence is waited for.
    await expect.poll(async () => (await drawnIds()).filter(id => deleted.includes(id)), {
      timeout: 15000,
      message: 'a deleted row is still drawn after the reload',
    }).toEqual([]);

    const drawn = await drawnIds();
    expect(new Set(drawn).size, 'an entry is drawn twice').toBe(drawn.length);
    for (const id of surviving) {
      expect(drawn.includes(id), `the row ${id} is stored but not drawn`).toBe(true);
    }
    // A prefix compare, not equality: the stats footer (model · tokens · elapsed) renders
    // once the turn fully settles, so the same message's textContent grows over time.
    expect((await page.locator('.chat-message.assistant').first().textContent())?.trim() || '').toContain(
      firstText.slice(0, 40),
    );
  });
});
