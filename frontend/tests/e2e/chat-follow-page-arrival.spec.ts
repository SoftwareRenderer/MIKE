import { test, expect, type Page } from '@playwright/test';
import {
  isolatedProject, gotoProject, getProjectId, csrfFetch, navigateToTask, waitForChatFormReady,
  waitForSendButton, sendMessage,
} from './helpers';

// The chat opens on the newest window and asks for the page above it when that window does not fill the
// chat. That read is the app's own, so it must not read as the reader leaving the bottom: while it is out,
// an answer arriving from the server is still something a reader at the bottom expects to see.
//
// The other tab sends the message, because a send pins its own chat to the bottom and would hide the
// answer.
isolatedProject();

const SEED_ROWS = 40;
/** Pinned mock fixture: 16 chunks at 100ms, so the turn streams rather than landing at once. */
const STREAMING_PROMPT = 'llm-fixture:text_slow';

async function seedConversation(page: Page, projectId: string, taskId: string, rows: number): Promise<void> {
  const csrf = await page.evaluate(async () => (await (await fetch('/api/auth/session')).json()).csrf_token || '');
  const seeded = await page.evaluate(async ({ url, count, token }) => {
    let made = 0;
    for (let i = 0; i < count; i++) {
      const res = await fetch(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', ...(token ? { 'X-CSRF-Token': token } : {}) },
        credentials: 'include',
        body: JSON.stringify({ role: 'user', content: `seed row ${i}\n${'filler text that gives the entry some height '.repeat(3)}` }),
      });
      if (!res.ok) return { made, status: res.status, text: await res.text() };
      made++;
    }
    return { made, status: 200, text: '' };
  }, { url: `/api/projects/${projectId}/tasks/${taskId}/messages`, count: rows, token: csrf });
  expect(seeded.status, `seeding stopped at row ${seeded.made}: ${seeded.text}`).toBe(200);
}

function position(page: Page): Promise<{ fromBottom: number; scrollTop: number; scrollHeight: number }> {
  return page.evaluate(() => {
    const el = document.querySelector('.chat-messages') as HTMLElement;
    return {
      fromBottom: Math.round(el.scrollHeight - el.scrollTop - el.clientHeight),
      scrollTop: Math.round(el.scrollTop),
      scrollHeight: Math.round(el.scrollHeight),
    };
  });
}

/** Sample how far the viewport sits above the bottom every 50ms until stopped. */
async function startSampler(page: Page): Promise<void> {
  await page.evaluate(() => {
    const w = window as any;
    w.__samples = [] as Array<{ fromBottom: number; height: number }>;
    w.__stop = () => clearInterval(w.__id);
    w.__id = setInterval(() => {
      const el = document.querySelector('.chat-messages') as HTMLElement | null;
      if (!el) return;
      w.__samples.push({
        fromBottom: Math.round(el.scrollHeight - el.scrollTop - el.clientHeight),
        height: el.scrollHeight,
      });
    }, 50);
  });
}

async function stopSampler(page: Page): Promise<Array<{ fromBottom: number; height: number }>> {
  return page.evaluate(() => {
    const w = window as any;
    w.__stop();
    return w.__samples;
  });
}

test.describe('An older page the app asked for', () => {
  test('does not stop the chat following what arrives while it is out', async ({ page }) => {
    test.setTimeout(180000);
    await page.setViewportSize({ width: 1280, height: 900 });
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
    const projectId = await getProjectId(page);
    const created = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
      method: 'POST',
      body: { title: `page-arrival-${Date.now()}`, prompt: 'page arrival fixture', status: 'in_progress' },
    });
    expect(created.ok, `create task: ${JSON.stringify(created.data)}`).toBe(true);
    const taskId: string = created.data.id;
    await seedConversation(page, projectId, taskId, SEED_ROWS);

    // Hold the page of older rows: the reader waits in the state that read put them in, with the
    // answer still arriving.
    let paged = false;
    await page.route(url => url.searchParams.has('before'), () => { paged = true; });
    await navigateToTask(page, taskId, projectId);
    await waitForChatFormReady(page);
    await expect(page.locator('.chat-messages [data-entry-id]').first()).toBeVisible({ timeout: 15000 });
    await waitForSendButton(page);
    // The newest window is shorter than the chat, so the app went looking for the rows above it.
    await expect.poll(() => paged, 'the app asked for a page of older rows to fill the chat').toBe(true);
    await page.waitForTimeout(600);

    // A second tab works the task; this one only watches, so nothing here pins itself.
    const worker = await page.context().newPage();
    await navigateToTask(worker, taskId, projectId);
    await waitForChatFormReady(worker);
    await waitForSendButton(worker);
    await startSampler(page);
    const turn = `answered-${Date.now()} ${STREAMING_PROMPT}`;
    await sendMessage(worker, turn);
    await expect(page.locator('.btn-stop'), 'the watching tab saw the turn start').toBeVisible({ timeout: 30000 });
    await waitForSendButton(worker, 60000);
    await waitForSendButton(page, 60000);
    await page.waitForTimeout(800);
    const samples = await stopSampler(page);
    const end = await position(page);
    const worst = samples.reduce((m, s) => Math.max(m, s.fromBottom), 0);

    await expect(page.locator('.chat-messages .chat-message.assistant').last(), 'the watching tab rendered the answer')
      .toContainText(/[a-z]/i);

    expect(
      worst,
      `the watching tab drifted ${worst}px above the bottom of a ${end.scrollHeight}px transcript while `
      + `an answer arrived (${samples.length} samples, ending ${end.fromBottom}px above the bottom)`,
    ).toBeLessThan(300);
    expect(end.fromBottom, `the watching tab ended ${end.fromBottom}px above the bottom`).toBeLessThan(40);

    await worker.close();
  });
});
