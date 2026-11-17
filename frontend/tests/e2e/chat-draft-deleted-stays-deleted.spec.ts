import { test, expect, type Page, type Locator } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, ensureTaskStarted, isolatedProject, gotoProject, sendMessage, waitForTurnAnswered, csrfFetch, clickCard } from './helpers';

// A turn runs in this file's tasks, and turns are queued per project: own the project.
isolatedProject();

test.describe.configure({ mode: 'serial' });

// The drafts store holds what the box holds, empty included, so a text the person deleted has to leave the
// store too — or the next read paints it back.

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

/** The box's value, read `times` 200ms apart. Sampled rather than asserted once, because the rebuild that
 * puts a deleted draft back can land between two assertions. Await it after starting it to sample across
 * another wait. */
function sampleBox(page: Page, times: number): Promise<string[]> {
  return (async () => {
    const seen: string[] = [];
    for (let i = 0; i < times; i++) {
      seen.push(await boxValue(page));
      await page.waitForTimeout(200);
    }
    return seen;
  })();
}

/** The readings that hold text — `absent` is what the task view shows in the box's place. */
const filledSamples = (samples: string[]) => samples.filter(v => v !== '' && v !== 'absent');

/** Delays every read of the `drafts` store, so a re-read is still in flight when the person deletes the
 * text; without the delay that window is a millisecond wide. `stopDelayingDraftReads` puts the prototype
 * back, so the assertions after the deletion read for real. */
async function delayDraftReads(page: Page, ms: number): Promise<void> {
    await page.evaluate((delay) => {
        const w = window as unknown as {
            __draftReads: number;
            __origGet?: IDBObjectStore['get'];
        };
        w.__draftReads = 0;
        const proto = IDBObjectStore.prototype;
        const origGet = proto.get;
        w.__origGet = origGet;
        proto.get = function (this: IDBObjectStore, key: IDBValidKey) {
            if (this.name !== 'drafts') return origGet.call(this, key);
            w.__draftReads++;
            const real = origGet.call(this, key);
            const delayed: { result: unknown; onsuccess: (() => void) | null; onerror: (() => void) | null } =
                { result: undefined, onsuccess: null, onerror: null };
            real.onsuccess = () => {
                delayed.result = real.result;
                setTimeout(() => delayed.onsuccess?.(), delay);
            };
            real.onerror = () => setTimeout(() => delayed.onerror?.(), delay);
            return delayed as unknown as IDBRequest;
        };
    }, ms);
}

async function stopDelayingDraftReads(page: Page): Promise<void> {
    await page.evaluate(() => {
        const w = window as unknown as { __origGet?: IDBObjectStore['get'] };
        if (w.__origGet) IDBObjectStore.prototype.get = w.__origGet;
    });
}

/** How many reads of the `drafts` store the app has started since `delayDraftReads`. */
function draftReadCount(page: Page): Promise<number> {
    return page.evaluate(() => (window as unknown as { __draftReads: number }).__draftReads);
}

/** A board frame is what makes this screen re-read the draft: it replaces the task row the chat's props come
 * from. A title update is the cheapest one to send. */
async function sendBoardFrame(page: Page, projectId: string, taskId: string): Promise<void> {
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}`, {
        method: 'PUT',
        body: { title: `board-frame-${Date.now()}` },
    });
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
    await clickCard(page, awayId);
    await expect(page.locator('.chat-form textarea')).toBeVisible();
    await clickCard(page, taskId);
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
    const sampling = sampleBox(page, 40);
    await waitForTurnAnswered(page, projectId, openTaskId(page));
    const samples = await sampling;

    expect(filledSamples(samples), 'the agent answered and the box holds text again').toEqual([]);
    await expect(page.locator('.chat-form textarea')).toHaveValue('');
  });

  test('stays deleted when a re-read was in flight while it was deleted', async ({ page }) => {
    const projectId = await getProjectId(page);
    const textarea = await openTask(page, projectId);
    const taskId = openTaskId(page);

    await typeAndLetItSave(page, textarea, 'thought deleted mid-read');
    await delayDraftReads(page, 1500);

    // The deletion lands while that re-read is still on its way.
    const readsBefore = await draftReadCount(page);
    await sendBoardFrame(page, projectId, taskId);
    await expect
        .poll(() => draftReadCount(page), { message: 'the board frame re-ran the draft read' })
        .toBeGreaterThan(readsBefore);
    await deleteAll(textarea);
    await stopDelayingDraftReads(page);

    expect(filledSamples(await sampleBox(page, 12)),
        'the read that was in flight painted the deleted text back').toEqual([]);

    await expect.poll(() => storedDraft(page, taskId),
      { message: 'the deletion reaches the drafts store', timeout: 15000 }).toBeNull();
  });
});
