import { test, expect } from '@playwright/test';
import { getProjectId, csrfFetch, navigateToTask, createTaskViaApi } from './helpers';

// ============================================================
// TASK HEADER PATH TOOLTIP (MOBILE)
//
// The Task View header shows the task's worktree path between the
// sandbox button and the status badge, truncated with an ellipsis on
// narrow screens. Its native `title` tooltip only appears on hover,
// which mobile taps never trigger — so on mobile (≤768px), tapping
// the path must show the full path as a tooltip. The tooltip:
//   - shows below the label, clamped inside the viewport,
//   - toggles off when the path is tapped again,
//   - dismisses on an outside tap,
//   - auto-hides after ~2.5 s,
//   - never opens on desktop (hover title is the desktop affordance).
// ============================================================

test.describe('Task header path tooltip (mobile)', () => {
  test.describe.configure({ mode: 'serial' });

  test('tapping the header path shows the full path as a tooltip', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    const projectId = await getProjectId(page);
    const ts = Date.now();
    const taskId = await createTaskViaApi(page, projectId, `Path tooltip mobile ${ts}`);

    try {
      await navigateToTask(page, taskId, projectId);

      // Mobile viewport (repo convention for mobile specs)
      await page.setViewportSize({ width: 390, height: 844 });

      const pathLabel = page.locator('.task-detail-page .project-prefix').first();
      await expect(pathLabel).toBeVisible({ timeout: 10000 });
      const fullPath = await pathLabel.getAttribute('title');
      expect(fullPath, 'path label must carry the full path in its title').toBeTruthy();

      const tooltip = page.locator('.path-tooltip');
      const tipText = tooltip.locator('.path-tooltip-text');

      // --- Tap the path → tooltip with the full path, clamped in the viewport ---
      await pathLabel.click();
      await expect(tooltip).toBeVisible();
      await expect(tipText).toHaveText(fullPath!);
      await expect(async () => {
        const box = (await tooltip.boundingBox())!;
        const labelBox = (await pathLabel.boundingBox())!;
        // Below the label…
        expect(box.y).toBeGreaterThanOrEqual(labelBox.y + labelBox.height);
        // …and horizontally inside the viewport (8px margin, minus rounding).
        expect(box.x).toBeGreaterThanOrEqual(7.5);
        expect(box.x + box.width).toBeLessThanOrEqual(390 - 7.5);
      }).toPass({ timeout: 2000 });

      // --- Tap the path again → tooltip toggles off ---
      await pathLabel.click();
      await expect(tooltip).toHaveCount(0);

      // --- Outside tap → tooltip dismisses (status badge: neutral target) ---
      await pathLabel.click();
      await expect(tooltip).toBeVisible();
      await page.locator('.task-detail-page .header-main .badge').first().click();
      await expect(tooltip).toHaveCount(0);

      // --- Copy button → full path on the clipboard, "Copied" confirmation,
      //     tooltip stays open and then auto-hides (timer re-armed on copy) ---
      await page.context().grantPermissions(['clipboard-read', 'clipboard-write']);
      await pathLabel.click();
      await expect(tooltip).toBeVisible();
      const copyBtn = tooltip.locator('.path-tooltip-copy');
      await copyBtn.click();
      await expect(copyBtn).toContainText('Copied', { timeout: 5000 });
      expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(fullPath);
      // Still open after the confirmation, then the re-armed auto-hide fires.
      await expect(tooltip).toBeVisible();
      await expect(tooltip).toHaveCount(0, { timeout: 4000 });

      // --- Desktop: tapping the path does nothing (native hover title) ---
      await page.setViewportSize({ width: 1280, height: 720 });
      await pathLabel.click();
      await expect(tooltip).toHaveCount(0);
    } finally {
      await page.setViewportSize({ width: 1280, height: 720 });
      await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}`, { method: 'DELETE' });
    }
  });
});
