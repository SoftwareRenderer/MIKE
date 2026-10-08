import { test, expect, type Page } from '@playwright/test';
import {
  clearTasksAfterEach, createTaskViaApi, isolatedProject, navigateToTask,
  ownProjectId, waitForChatForm, PENDING_TOOL_STATUS as PENDING,
} from './helpers';

// A pending tool call's header is one flex row: icon, tool name, arguments, status. Arguments longer than
// `ARGS_MAX_LEN` (50) are wrapped in a `<button>`, and a flex item's automatic minimum size is its content,
// so that button kept its 300px ellipsized width instead of giving way. The status — the last item, and the
// longest of them once the elapsed counter starts — was drawn outside the card, which the chat scroller
// clips with `overflow-x: hidden`. Measured before the fix at a 390px viewport: the status drawn 91px past
// its card, and 293px past it for a tool name with no break opportunity (`mcp_…`); at every width the row
// squeezed the status onto two or three lines first. A call with short arguments was never affected, which
// is why the report read as "sometimes".
//
// Overflow is layout and jsdom has no layout engine, so this is measured in a browser — the shape of
// `explorer-header-overflow.spec.ts`, with two differences this card forces:
//   - the viewport is set BEFORE the first navigation, because the phone layout (off-canvas sidebar, single
//     column) is entered when the app mounts; a desktop-mounted app resized to 320 keeps its desktop columns.
//   - one test per mount mode, because the elapsed counter exists only in the session that saw the call
//     start (`startedAt` comes off the live stream, never off loaded history), so the widths are measured by
//     resizing a live turn rather than by reloading at each one.

isolatedProject();
// Every test of this file runs a turn, and a live task is charged its container's memory against the
// executor's admission gate until it is released.
clearTasksAfterEach();

/**
 * '' when the pending card's status is drawn where a reader can see it.
 *
 * The probe's arguments (59 chars) are truncated on purpose: the ellipsized box is the flex item that would
 * not shrink, so it has to stay in the row for this to measure anything. It must give way by truncating —
 * its text still wider than its box — rather than by taking the status with it.
 */
async function headerProblems(page: Page): Promise<string> {
  return page.evaluate(() => {
    const status = document.querySelector('.tool-status.pending');
    const card = status?.closest('.tool-call-card');
    const header = card?.querySelector('.tool-call-header');
    const args = header?.querySelector('.tool-args');
    if (!status || !card || !header || !args) return 'no pending tool-call header on screen';
    const problems: string[] = [];
    const box = status.getBoundingClientRect();
    const cardBox = card.getBoundingClientRect();
    if (Math.round(box.right) > Math.round(cardBox.right)) {
      problems.push(`status drawn ${Math.round(box.right) - Math.round(cardBox.right)}px past its card`);
    }
    if (Math.round(box.right) > window.innerWidth || Math.round(box.left) < 0) {
      problems.push(`status drawn at ${Math.round(box.left)}..${Math.round(box.right)}, outside 0..${window.innerWidth}`);
    }
    if (header.scrollWidth > header.clientWidth) {
      problems.push(`header spills ${header.scrollWidth - header.clientWidth}px of its row`);
    }
    // One line is ~18px here; two is ~33px. A wrapped status is the same squeeze one step before the spill.
    if (box.height > 22) problems.push(`status wrapped to ${Math.round(box.height)}px tall`);
    const argsBox = args.getBoundingClientRect();
    if (argsBox.width < 24) problems.push(`arguments squeezed to ${Math.round(argsBox.width)}px`);
    if (args.scrollWidth <= args.clientWidth) problems.push('long arguments were not truncated');
    return problems.join('; ');
  });
}

/**
 * Start the probe turn and wait for the elapsed line.
 *
 * `tool_call_long_args_sleep` is one bash call whose command is long enough to be ellipsized and which
 * sleeps 20s, so the counter is on screen for the pass over the widths.
 */
async function startProbe(page: Page): Promise<void> {
  const projectId = ownProjectId();
  const taskId = await createTaskViaApi(
    page, projectId, `tool-header-overflow-${Date.now()} llm-fixture:tool_call_long_args_sleep`);
  await navigateToTask(page, taskId, projectId);
  await waitForChatForm(page);
  await expect(page.locator(PENDING), 'the probe turn never reached its tool call')
    .toBeVisible({ timeout: 90000 });
  // The line the report names; it starts 5s into the call, which has 15s left after that.
  await expect(page.locator('.tool-status.pending .progress-text'), 'the elapsed counter never appeared')
    .toContainText('Running for', { timeout: 20000 });
}

test.describe.serial('a running tool call keeps its elapsed counter on screen', () => {
  test('mounted at a phone width, resized across the phone widths', async ({ page }) => {
    test.setTimeout(180000);
    // Before the first navigation: the phone layout is chosen when the app mounts.
    await page.setViewportSize({ width: 320, height: 800 });
    await startProbe(page);

    for (const width of [320, 360, 390, 430, 700]) {
      await page.setViewportSize({ width, height: 800 });
      await expect(() => headerProblems(page), `${width}px: the elapsed counter is on screen`)
        .resolves.toBe('');
    }
  });

  test('mounted at a desktop width, resized across the desktop widths', async ({ page }) => {
    test.setTimeout(180000);
    await page.setViewportSize({ width: 1280, height: 900 });
    await startProbe(page);

    // The row never spilled here, but it wrapped the status onto two lines: the same squeeze, one step
    // earlier. 1024 is where the chat column is narrowest beside the sidebar.
    for (const width of [1280, 1024]) {
      await page.setViewportSize({ width, height: 900 });
      await expect(() => headerProblems(page), `${width}px: the elapsed counter is on one line`)
        .resolves.toBe('');
    }
  });
});
