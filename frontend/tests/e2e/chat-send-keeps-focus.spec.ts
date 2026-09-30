import { test, expect, Page, ElementHandle } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, waitForChatForm, isolatedProject, gotoProject } from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();

test.describe.configure({ mode: 'serial' });

// ============================================================
// CHAT INPUT — THE CARET SURVIVES A SEND (Task View, desktop)
// ============================================================
// Reading the answer and typing the next message are one continuous gesture: the caret stays in the input the
// user typed into — the same element, not a fresh copy of it. Two things take it: a click hands focus to
// ➤ Send, which the announced turn then replaces with ■ Stop, and a task that only looked stalled trades the
// whole form for the Restart bar. (jsdom moves no focus on click, so the click case lives here.)

/** The task at rest with its chat input ready, the way the desktop user reaches it. */
async function openIdleTask(page: Page, label: string): Promise<void> {
  await gotoProject(page);
  const projectId = await getProjectId(page);
  const taskId = await createTaskViaApi(page, projectId, `${label}-${Date.now()}`);
  await navigateToTask(page, taskId);
  await waitForChatForm(page);
  // The task's own opening turn may still be running; the input is idle at ➤ Send.
  await expect(page.locator('.btn-send'), 'the task is idle before sending').toBeVisible({ timeout: 60000 });
}

/** Assert the element the user typed into — not a replacement of it — holds the focus. */
async function expectSameInputFocused(page: Page, input: ElementHandle<Element>): Promise<void> {
  await expect
    .poll(() => page.evaluate((el) => document.activeElement === el, input), 'the caret stays where it was typed')
    .toBe(true);
}

/** Type a message without sending it, and hand back the element behind the input. */
async function typeMessage(page: Page, text: string): Promise<ElementHandle<Element>> {
  const input = page.locator('#chat-message-input');
  await input.click();
  await input.fill(text);
  return (await input.elementHandle())!;
}

test.describe('Chat input keeps the caret through a send', () => {
  test('Enter sends and the turn that follows leaves the caret alone', async ({ page }) => {
    test.setTimeout(90000);
    await openIdleTask(page, `caret-enter`);
    const message = `caret after enter ${Date.now()}`;
    const input = await typeMessage(page, message);

    await page.locator('#chat-message-input').press('Enter');
    await expect(page.locator('.chat-message.user').last()).toContainText(message);

    // The announced turn swaps ➤ Send for ■ Stop and the answer lands; the caret does not move.
    await expect(page.locator('.chat-message.assistant').last()).toBeVisible();
    await expect(page.locator('.btn-send'), 'the turn ended').toBeVisible({ timeout: 60000 });
    await expectSameInputFocused(page, input);
  });

  test('clicking Send leaves the caret in the input, not on the button it replaced', async ({ page }) => {
    test.setTimeout(90000);
    await openIdleTask(page, `caret-click`);
    const message = `caret after click ${Date.now()}`;
    const input = await typeMessage(page, message);

    await page.locator('.btn-send').click();
    await expect(page.locator('.chat-message.user').last()).toContainText(message);

    await expect(page.locator('.chat-message.assistant').last()).toBeVisible();
    await expect(page.locator('.btn-send'), 'the turn ended').toBeVisible({ timeout: 60000 });
    await expectSameInputFocused(page, input);
  });
});
