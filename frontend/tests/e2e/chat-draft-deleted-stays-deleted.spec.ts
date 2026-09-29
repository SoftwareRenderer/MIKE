import { test, expect, type Page, type Locator } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, ensureTaskStarted, isolatedProject, gotoProject, sendMessage, waitForTurnAnswered } from './helpers';

// A turn runs in this file's tasks, and turns are queued per project: own the project.
isolatedProject();

test.describe.configure({ mode: 'serial' });

// The drafts store holds what the input box holds, empty included: text the person deleted has to leave
// the store too, or the next read of the draft paints it back into the box.

function openTaskId(page: Page): string {
  return new URL(page.url()).searchParams.get('task')!;
}

/** What the `drafts` store holds for this task: the text, or null when it holds nothing. */
function storedDraft(page: Page, taskId: string): Promise<string | null> {
  return page.evaluate(async (tid) => new Promise<string | null>((resolve) => {
    const open = indexedDB.open('kanban-storage');
    open.onsuccess = () => {
      const db = open.result;
      const request = db.transaction('drafts', 'readonly').objectStore('drafts').get(tid);
      request.onsuccess = () => resolve(request.result === undefined ? null : request.result);
      request.onerror = () => resolve(null);
    };
    open.onerror = () => resolve(null);
  }), taskId);
}

/** The value of the chat input, or `absent` while the task view shows something else in its place. */
function boxValue(page: Page): Promise<string> {
  return page.evaluate(() => document.querySelector<HTMLTextAreaElement>('.chat-form textarea')?.value ?? 'absent');
}

async function openTask(page: Page, projectId: string): Promise<Locator> {
  const taskId = await createTaskViaApi(page, projectId, `deleted-draft-${Date.now()}`);
  await navigateToTask(page, taskId, projectId);
  await ensureTaskStarted(page, projectId, taskId);
  const textarea = page.locator('.chat-form textarea');
  await expect(textarea).toBeVisible();
  return textarea;
}

async function typeAndLetItSave(page: Page, textarea: Locator, text: string): Promise<void> {
  await textarea.click();
  await textarea.pressSequentially(text);
  await expect(textarea).toHaveValue(text);
  await expect.poll(() => storedDraft(page, openTaskId(page)),
      { message: 'the typed text reaches the drafts store' }).toBe(text);
}

/** Deletes the way a person does: select all, backspace. */
async function deleteAll(textarea: Locator): Promise<void> {
  await textarea.click();
  await textarea.press('ControlOrMeta+a');
  await textarea.press('Backspace');
  await expect(textarea).toHaveValue('');
}

test.describe('Chat draft text the person deleted', () => {
  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.project-item');
  });

  test('stays deleted across a reload', async ({ page }) => {
    const projectId = await getProjectId(page);
    const textarea = await openTask(page, projectId);

    await typeAndLetItSave(page, textarea, 'text that will be deleted');
    await deleteAll(textarea);

    await expect.poll(() => storedDraft(page, openTaskId(page)),
        { message: 'the emptied box empties the drafts store' }).toBeNull();

    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();
    await expect(page.locator('.chat-form textarea')).toHaveValue('');
  });

  test('stays deleted when the task view is reopened without a reload', async ({ page }) => {
    const projectId = await getProjectId(page);
    const textarea = await openTask(page, projectId);
    const taskId = openTaskId(page);
    const awayId = await createTaskViaApi(page, projectId, `switch-away-${Date.now()}`);
    await ensureTaskStarted(page, projectId, awayId);

    await typeAndLetItSave(page, textarea, 'deleted before switching away');
    await deleteAll(textarea);

    // A card click is a client-side switch: no reload, and this task's chat is rebuilt on the way back.
    await page.locator(`[data-task-id="${awayId}"]`).click();
    await expect(page.locator('.chat-form textarea')).toBeVisible();
    await page.locator(`[data-task-id="${taskId}"]`).click();
    await expect(page.locator('.chat-form textarea')).toBeVisible();

    await expect(page.locator('.chat-form textarea'), 'the reopened box holds text nobody deleted')
        .toHaveValue('');
  });

  test('does not come back into the box while the agent answers', async ({ page }) => {
    const projectId = await getProjectId(page);
    const textarea = await openTask(page, projectId);

    // A message for the agent to answer, so messages arrive around the deletion.
    await sendMessage(page, 'write a note');
    await waitForTurnAnswered(page, projectId, openTaskId(page));

    await typeAndLetItSave(page, textarea, 'half-written thought');
    await deleteAll(textarea);

    await sendMessage(page, 'and another one');
    // Sampled while the turn runs rather than asserted once after it, because the rebuilds a turn causes
    // are what put the text back. The interval is a sampling cadence, not a wait for anything.
    const sampling = (async () => {
      const seen: string[] = [];
      for (let i = 0; i < 40; i++) {
        seen.push(await boxValue(page));
        await page.waitForTimeout(200); // sampling cadence: the samples are the measurement, not a wait
      }
      return seen;
    })();
    await waitForTurnAnswered(page, projectId, openTaskId(page));
    const samples = await sampling;

    expect(samples.filter(v => v !== '' && v !== 'absent'), 'the agent answered and the box holds text again')
        .toEqual([]);
    await expect(page.locator('.chat-form textarea')).toHaveValue('');
  });
});
