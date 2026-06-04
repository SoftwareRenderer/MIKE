import { test, expect } from '@playwright/test';

test.describe('Mobile Responsive Features', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item, .card', { timeout: 10000 });
  });

  // --- Board Tests ---

  test('should have horizontally scrollable board with scroll-snap', async ({ page }) => {
    const board = page.locator('.board');
    await expect(board).toBeVisible();

    // Check scroll-snap CSS property is applied
    const snapType = await board.evaluate(el => getComputedStyle(el).scrollSnapType);
    expect(snapType).toContain('x mandatory');

    // Check overflow-x is auto
    const overflowX = await board.evaluate(el => getComputedStyle(el).overflowX);
    expect(overflowX).toBe('auto');
  });

  test('should disable drag-and-drop on cards', async ({ page }) => {
    await page.waitForSelector('.card', { timeout: 10000 });
    const card = page.locator('.card').first();
    const draggable = await card.getAttribute('draggable');
    expect(draggable).toBe('false');
  });

  // --- Sidebar Tests ---

  test('should show hamburger button on mobile', async ({ page }) => {
    const hamburger = page.locator('.hamburger');
    await expect(hamburger).toBeVisible();
  });

  test('should open sidebar drawer when hamburger is clicked', async ({ page }) => {
    const hamburger = page.locator('.hamburger');
    await hamburger.click();

    // Sidebar should have mobile-open class
    const sidebar = page.locator('.sidebar');
    await expect(sidebar).toHaveClass(/mobile-open/);

    // Backdrop should be visible
    const backdrop = page.locator('.sidebar-backdrop');
    await expect(backdrop).toBeVisible();
  });

  test('should close sidebar when backdrop is clicked', async ({ page }) => {
    await page.locator('.hamburger').click();
    await expect(page.locator('.sidebar')).toHaveClass(/mobile-open/);

    // Click backdrop
    await page.locator('.sidebar-backdrop').click();

    // Sidebar should no longer have mobile-open class
    const sidebar = page.locator('.sidebar');
    const classes = await sidebar.getAttribute('class');
    expect(classes).not.toContain('mobile-open');
  });

  test('should close sidebar when a project is selected', async ({ page }) => {
    // Open sidebar
    await page.locator('.hamburger').click();
    await expect(page.locator('.sidebar')).toHaveClass(/mobile-open/);

    // Click a project
    const projectItem = page.locator('.project-item').first();
    await projectItem.click();

    // Sidebar should close
    const sidebar = page.locator('.sidebar');
    const classes = await sidebar.getAttribute('class');
    expect(classes).not.toContain('mobile-open');
  });

  // --- Task Detail Tests ---

  test('should show mobile tab bar in task detail', async ({ page }) => {
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    const tabBar = page.locator('.mobile-tab-bar');
    await expect(tabBar).toBeVisible();

    // Should have 3 tabs
    const tabs = tabBar.locator('.tab-btn');
    expect(await tabs.count()).toBe(3);

    // Tab labels
    const tabTexts = await tabs.allTextContents();
    const trimmedTexts = tabTexts.map(t => t.trim());
    expect(trimmedTexts).toContain('Context');
    expect(trimmedTexts).toContain('Agent');
    expect(trimmedTexts).toContain('Changes');
  });

  test('should switch content when clicking tabs', async ({ page }) => {
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    const tabBar = page.locator('.mobile-tab-bar');
    const tabs = tabBar.locator('.tab-btn');

    // Click Context tab (first tab)
    await tabs.nth(0).click();
    await expect(tabs.nth(0)).toHaveClass(/active/);
    // Context column should be visible
    await expect(page.locator('.column-context')).toBeVisible();

    // Click Changes tab (third tab)
    await tabs.nth(2).click();
    await expect(tabs.nth(2)).toHaveClass(/active/);
    // Diff column should be visible
    await expect(page.locator('.column-diff')).toBeVisible();

    // Click Agent tab (second tab)
    await tabs.nth(1).click();
    await expect(tabs.nth(1)).toHaveClass(/active/);
    // Chat column should be visible
    await expect(page.locator('.column-chat')).toBeVisible();
  });

  test('should switch tabs on swipe gesture', async ({ page }) => {
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    const columns = page.locator('.columns');
    const boundingBox = await columns.boundingBox();
    if (!boundingBox) {
      test.skip('Could not get columns bounding box');
      return;
    }

    // Default active tab should be Agent (middle)
    const tabs = page.locator('.mobile-tab-bar .tab-btn');
    const activeTab = await tabs.filter({ hasText: 'Agent' }).getAttribute('class');
    expect(activeTab).toContain('active');

    // Swipe left (drag from right to left) — should go to Changes
    const startX = boundingBox.x + boundingBox.width - 50;
    const endX = boundingBox.x + 50;
    const y = boundingBox.y + boundingBox.height / 2;

    await page.mouse.move(startX, y);
    await page.mouse.down();
    await page.mouse.move(endX, y);
    await page.mouse.up();

    await page.waitForTimeout(200);

    // Changes tab should be active
    const changesTab = tabs.filter({ hasText: 'Changes' });
    const changesClass = await changesTab.getAttribute('class');
    expect(changesClass).toContain('active');

    // Swipe right — should go back to Agent
    const swipeRightStart = boundingBox.x + 50;
    const swipeRightEnd = boundingBox.x + boundingBox.width - 50;

    await page.mouse.move(swipeRightStart, y);
    await page.mouse.down();
    await page.mouse.move(swipeRightEnd, y);
    await page.mouse.up();

    await page.waitForTimeout(200);

    // Agent tab should be active again
    const agentTab = tabs.filter({ hasText: 'Agent' });
    const agentClass = await agentTab.getAttribute('class');
    expect(agentClass).toContain('active');
  });

  // --- Card Tests ---

  test('should have cards without quick action buttons', async ({ page }) => {
    await page.waitForSelector('.card', { timeout: 10000 });
    const card = page.locator('.card').first();

    // Quick actions should not exist (removed for conversational model)
    const quickActions = card.locator('.quick-actions');
    expect(await quickActions.count()).toBe(0);

    // Card should still be clickable to open task detail
    await card.click();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });
  });

  // --- Modal Tests ---

  test('should show CreateTaskModal full-screen on mobile', async ({ page }) => {
    await page.click('.add-task-btn');
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    // Modal should take full viewport
    const modal = page.locator('.modal');
    const boundingBox = await modal.boundingBox();
    const viewport = page.viewportSize();

    expect(boundingBox?.width).toBe(viewport?.width);
    expect(boundingBox?.height).toBe(viewport?.height);
  });

  test('should show ConfigModal full-screen on mobile', async ({ page }) => {
    await page.click('[title="Settings"]');
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    const modal = page.locator('.modal');
    const boundingBox = await modal.boundingBox();
    const viewport = page.viewportSize();

    expect(boundingBox?.width).toBe(viewport?.width);
    expect(boundingBox?.height).toBe(viewport?.height);
  });
});
