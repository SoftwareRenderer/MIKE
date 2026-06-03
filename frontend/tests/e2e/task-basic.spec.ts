import { test, expect } from '@playwright/test';

test.describe('Task Management', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    // Wait for the app to load
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should display the board with columns', async ({ page }) => {
    await expect(page.locator('.board')).toBeVisible();
    await expect(page.locator('.column')).toHaveCount(4);
  });

  test('should create a new task', async ({ page }) => {
    // Click the add task button
    await page.click('.add-task-btn');
    
    // Wait for the modal to appear
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });
    
    // Fill in the prompt
    await page.fill('textarea', 'Test task from e2e test');
    
    // Submit
    await page.click('button[type="submit"]');
    
    // Wait for modal to close
    await expect(page.locator('.modal')).not.toBeVisible();
    
    // Wait for task to appear in backlog
    await page.waitForSelector('.card', { timeout: 5000 });
    
    // Verify at least one task exists
    const cards = page.locator('.card');
    await expect(cards.first()).toBeVisible();
  });

  test('should open task detail view', async ({ page }) => {
    // Wait for at least one card to exist
    await page.waitForSelector('.card', { timeout: 10000 });
    
    // Click on the first card
    await page.click('.card');
    
    // Verify task detail view is shown
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.title')).toBeVisible();
  });

  test('should navigate back to board from task detail', async ({ page }) => {
    // Wait for at least one card to exist
    await page.waitForSelector('.card', { timeout: 10000 });
    
    // Open task
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });
    
    // Click back button
    await page.click('.btn-back');
    
    // Verify board is shown again
    await expect(page.locator('.board')).toBeVisible();
    await expect(page.locator('.task-detail-page')).not.toBeVisible();
  });

  test('should show sidebar hidden when task detail is open', async ({ page }) => {
    // Verify sidebar is visible
    await expect(page.locator('.sidebar')).toBeVisible();
    
    // Wait for at least one card to exist
    await page.waitForSelector('.card', { timeout: 10000 });
    
    // Open task
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });
    
    // Verify sidebar is hidden
    await expect(page.locator('.sidebar')).not.toBeVisible();
  });

  test('should have 3-column layout in task detail', async ({ page }) => {
    // Wait for at least one card to exist
    await page.waitForSelector('.card', { timeout: 10000 });
    
    // Open task
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });
    
    // Verify 3 columns exist
    const columns = page.locator('.columns .column');
    await expect(columns).toHaveCount(3);
    
    // Verify column headers
    await expect(page.locator('.column-context .column-title')).toBeVisible();
    await expect(page.locator('.column-chat .column-title')).toContainText('Agent');
    await expect(page.locator('.column-diff .column-title')).toContainText('Changes');
  });

  test('should show chat interface in task detail', async ({ page }) => {
    // Wait for at least one card to exist
    await page.waitForSelector('.card', { timeout: 10000 });
    
    // Open task
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });
    
    // Verify chat form is visible
    await expect(page.locator('.chat-form')).toBeVisible();
    await expect(page.locator('textarea[placeholder*="message"]')).toBeVisible();
  });

  test('should send a message in chat', async ({ page }) => {
    // Wait for at least one card to exist
    await page.waitForSelector('.card', { timeout: 10000 });
    
    // Open task
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });
    
    // Wait for WebSocket to connect
    await page.waitForTimeout(3000);
    
    // Type and send a message
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('Hello from e2e test');
    await chatTextarea.press('Enter');
    
    // Wait for the textarea to be cleared (message sent)
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });
    
    // Message was sent successfully (textarea cleared)
    // The echo from server may take time to appear
  });
});
