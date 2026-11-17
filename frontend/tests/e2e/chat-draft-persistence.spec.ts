import { test, expect, type Page, type Locator } from '@playwright/test';
import {
  getProjectId, createTaskViaApi, navigateToTask, ensureTaskStarted, isolatedProject, gotoProject,
  sendMessage, waitForTurnAnswered, waitForTaskState, notInFlight, waitForSendButton, clearTasksAfterEach,
  readStored,
} from './helpers';

// The drafts store holds what the input box holds, empty included. Both directions are asked here: text the
// person typed comes back (a reload, or a task switched away from and back to), and text the person deleted
// has to leave the store too, or the next read of the draft paints it back into the box.
isolatedProject();
// Every test opens its own task, and a task holds its container against the executor's admission gate until
// it is deleted: see clearTasksAfterEach() in helpers.ts.
clearTasksAfterEach();

test.describe.configure({ mode: 'serial' });

/** The task this page is showing, from the address the task view put there. */
function openTaskId(page: Page): string {
  return new URL(page.url()).searchParams.get('task')!;
}

/** What the `drafts` store holds for this task: the text, or null when it holds nothing. */
function storedDraft(page: Page, taskId: string): Promise<string | null> {
  return readStored<string>(page, 'drafts', taskId);
}

/** The value of the chat input, or `absent` while the task view shows something else in its place. */
function boxValue(page: Page): Promise<string> {
  return page.evaluate(() => document.querySelector<HTMLTextAreaElement>('.chat-form textarea')?.value ?? 'absent');
}

/** Open a task and hand back its chat box. Pinned to `text`: the task's own turn must answer and end. */
async function openTask(page: Page, projectId: string, label: string): Promise<Locator> {
  const taskId = await createTaskViaApi(page, projectId, `${label}-${Date.now()} llm-fixture:text`);
  await navigateToTask(page, taskId, projectId);
  await ensureTaskStarted(page, projectId, taskId);
  const textarea = page.locator('.chat-form textarea');
  await expect(textarea).toBeVisible();
  return textarea;
}

/** The box ready for text: the task is idle, so a draft written now is not raced by a turn rebuilding it. */
async function idleBox(page: Page, projectId: string, taskId: string): Promise<Locator> {
  await waitForTaskState(page, projectId, taskId, notInFlight, 60000);
  await waitForSendButton(page);
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

/**
 * Click a card once the board has stopped animating a move. A card that just changed column stays on screen
 * while the node it left fades out, so `[data-task-id]` matches two elements for a moment after the move.
 */
async function clickCard(page: Page, taskId: string): Promise<void> {
  const card = page.locator(`.card[data-task-id="${taskId}"]`);
  await expect(card, 'the board holds one node for this task: the move it just made has finished fading')
      .toHaveCount(1);
  await card.click();
  await expect(page.locator('.task-detail-page')).toBeVisible();
}

test.describe('Chat draft text and the drafts store', () => {
  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.project-item');
  });

  test('text typed into the box survives a reload', async ({ page }) => {
    const projectId = await getProjectId(page);
    const textarea = await openTask(page, projectId, 'draft-reload');
    const taskId = openTaskId(page);
    await idleBox(page, projectId, taskId);

    const text = 'hello world draft test';
    await typeAndLetItSave(page, textarea, text);

    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();
    await expect(page.locator('.chat-form textarea')).toHaveValue(text);
  });

  test('text the person deleted empties the store and stays deleted across a reload', async ({ page }) => {
    const projectId = await getProjectId(page);
    const textarea = await openTask(page, projectId, 'draft-deleted');
    const taskId = openTaskId(page);
    await idleBox(page, projectId, taskId);

    await typeAndLetItSave(page, textarea, 'text that will be deleted');
    await deleteAll(textarea);

    await expect.poll(() => storedDraft(page, taskId),
        { message: 'the emptied box empties the drafts store' }).toBeNull();

    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();
    await expect(page.locator('.chat-form textarea'), 'the reload painted back text nobody typed')
        .toHaveValue('');
  });

  test('each task has its own draft, and a deleted one stays deleted across a task switch', async ({ page }) => {
    const projectId = await getProjectId(page);
    const textareaA = await openTask(page, projectId, 'draft-task-a');
    const taskIdA = openTaskId(page);
    await idleBox(page, projectId, taskIdA);
    await typeAndLetItSave(page, textareaA, 'draft for task A');

    // A second task, then the board: the card click is a client-side switch, so no reload rebuilds the
    // box and the two drafts are told apart only by the key each was written under.
    const taskIdB = await createTaskViaApi(page, projectId, `draft-task-b-${Date.now()} llm-fixture:text`);
    // A task still sitting on its Start bar has no chat box to hold a draft, so B is started the way the
    // other tasks of this file are before the box is asked for anything.
    await ensureTaskStarted(page, projectId, taskIdB);
    await gotoProject(page);
    await expect(page.locator(`.card[data-task-id="${taskIdB}"]`)).toHaveCount(1);
    await clickCard(page, taskIdB);

    // B holds no text: not A's, and none of its own yet.
    await expect(page.locator('.chat-form textarea'), "task B opened holding task A's text").toHaveValue('');
    await typeAndLetItSave(page, page.locator('.chat-form textarea'), 'text B will lose');
    await deleteAll(page.locator('.chat-form textarea'));
    await expect.poll(() => storedDraft(page, taskIdB),
        { message: 'the emptied box empties the drafts store' }).toBeNull();

    // Back to A: its draft is where the reader left it, and B stays empty.
    await gotoProject(page);
    await clickCard(page, taskIdA);
    await expect(page.locator('.chat-form textarea'), 'the reopened box lost the text it was left with')
        .toHaveValue('draft for task A');
    await gotoProject(page);
    await clickCard(page, taskIdB);
    await expect(page.locator('.chat-form textarea'), 'the reopened box holds text nobody deleted')
        .toHaveValue('');
  });

  test('deleted text does not come back into the box while the agent answers', async ({ page }) => {
    const projectId = await getProjectId(page);
    const textarea = await openTask(page, projectId, 'draft-during-turn');
    const taskId = openTaskId(page);
    await idleBox(page, projectId, taskId);

    // A message for the agent to answer, so messages arrive around the deletion.
    await sendMessage(page, `write a note ${Date.now()} llm-fixture:text`);
    await waitForTurnAnswered(page, projectId, taskId);

    await typeAndLetItSave(page, textarea, 'half-written thought');
    await deleteAll(textarea);

    await sendMessage(page, `and another one ${Date.now()} llm-fixture:text`);
    // Sampled while the turn runs rather than asserted once after it, because the rebuilds a turn causes
    // are what put the text back. The interval is a sampling cadence, not a wait for anything.
    const sampling = (async () => {
      const seen: string[] = [];
      for (let i = 0; i < 40; i++) {
        seen.push(await boxValue(page));
        await page.waitForTimeout(200);
      }
      return seen;
    })();
    await waitForTurnAnswered(page, projectId, taskId);
    const samples = await sampling;

    expect(samples.filter(v => v !== '' && v !== 'absent'), 'the agent answered and the box holds text again')
        .toEqual([]);
    await expect(page.locator('.chat-form textarea')).toHaveValue('');
  });
});
