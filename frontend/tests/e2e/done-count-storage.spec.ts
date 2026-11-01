import { test, expect } from '@playwright/test';
import { isolatedProject, gotoProject } from './helpers';

isolatedProject();

// The reading is pushed, so only the real server says the badge has anything to show. A tap is the
// gesture because a phone has no hover.
test.describe('the Done column count as the filesystem reading', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true });

  test('a tap on the count reveals the storage percentage, and the next tap hides it', async ({ page }) => {
    await gotoProject(page);

    const count = page.locator('.column[data-column-id="done"] .storage-count');
    await expect(count).toBeVisible({ timeout: 15000 });

    await expect(page.getByRole('tooltip')).toHaveCount(0);
    await count.tap();
    await expect(page.getByRole('tooltip')).toContainText('%');

    await count.tap();
    await expect(page.getByRole('tooltip')).toHaveCount(0);
  });
});
