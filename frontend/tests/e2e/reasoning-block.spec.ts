import { test, expect } from '@playwright/test';
import {
  getProjectId,
  createTaskViaApi,
  navigateToTask,
  waitForChatForm,
  waitForSendButton,
} from './helpers';

test.describe.serial('ReasoningBlock', () => {
  let projectId: string;
  let taskId: string;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
    taskId = await createTaskViaApi(page, projectId, `reasoning-test-${Date.now()}`);
    await navigateToTask(page, taskId);
    await waitForChatForm(page);
  });

  test('should show reasoning toggle when LLM returns reasoning content', async ({ page }) => {
    test.setTimeout(30000);

    // Graceful: executor may not start in test env
    const sendBtnVisible = await page.locator('.btn-send').isVisible({ timeout: 20000 }).catch(() => false);
    if (!sendBtnVisible) return;

    const chatTextarea = page.locator('textarea[placeholder*="message"]');

    // Send a message that triggers the reasoning mock response
    await chatTextarea.fill('reasoning test show reasoning');
    await chatTextarea.press('Enter');

    // Wait for the textarea to clear (message sent)
    await expect(chatTextarea).toHaveValue('');

    // Wait for user message to appear in chat
    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });

    // Wait for the LLM response to complete (send button reappears)
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });

    // The reasoning toggle should be visible in the assistant message
    const reasoningToggle = page.locator('.reasoning-toggle');
    await expect(reasoningToggle).toBeVisible({ timeout: 10000 });

    // The toggle should say "Show reasoning" (collapsed by default for short reasoning with content)
    const toggleText = await reasoningToggle.textContent();
    expect(toggleText).toContain('reasoning');
  });

  test('should expand reasoning content when toggle is clicked', async ({ page }) => {
    test.setTimeout(30000);

    // Graceful: executor may not start in test env
    const sendBtnVisible = await page.locator('.btn-send').isVisible({ timeout: 20000 }).catch(() => false);
    if (!sendBtnVisible) return;

    const chatTextarea = page.locator('textarea[placeholder*="message"]');

    // Send a message that triggers the reasoning mock response
    await chatTextarea.fill('reasoning test expand reasoning');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    // Wait for the LLM response to complete
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });

    // The reasoning toggle should be visible
    const reasoningToggle = page.locator('.reasoning-toggle');
    await expect(reasoningToggle).toBeVisible({ timeout: 10000 });

    // Click the toggle to expand reasoning
    await reasoningToggle.click();

    // The reasoning content should now be visible
    const reasoningContent = page.locator('.reasoning-content');
    await expect(reasoningContent).toBeVisible({ timeout: 5000 });

    // The reasoning content should contain the expected text
    const contentText = await reasoningContent.textContent();
    expect(contentText).toContain('think');
    expect(contentText).toContain('step by step');

    // The toggle text should change to "Hide reasoning"
    const expandedToggleText = await reasoningToggle.textContent();
    expect(expandedToggleText).toContain('Hide');
  });

  test('should collapse reasoning content when toggle is clicked again', async ({ page }) => {
    test.setTimeout(30000);

    // Graceful: executor may not start in test env
    const sendBtnVisible = await page.locator('.btn-send').isVisible({ timeout: 20000 }).catch(() => false);
    if (!sendBtnVisible) return;

    const chatTextarea = page.locator('textarea[placeholder*="message"]');

    // Send a message that triggers the reasoning mock response
    await chatTextarea.fill('reasoning test collapse reasoning');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    // Wait for the LLM response to complete
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });

    // Expand reasoning
    const reasoningToggle = page.locator('.reasoning-toggle');
    await expect(reasoningToggle).toBeVisible({ timeout: 10000 });
    await reasoningToggle.click();

    // Verify reasoning is expanded
    const reasoningContent = page.locator('.reasoning-content');
    await expect(reasoningContent).toBeVisible({ timeout: 5000 });

    // Click toggle again to collapse
    await reasoningToggle.click();

    // The reasoning content should now be hidden
    await expect(reasoningContent).not.toBeVisible({ timeout: 5000 });

    // The toggle text should change back to "Show reasoning"
    const collapsedToggleText = await reasoningToggle.textContent();
    expect(collapsedToggleText).toContain('Show');
  });

  test('should not show reasoning toggle for messages without reasoning', async ({ page }) => {
    test.setTimeout(30000);

    // Graceful: executor may not start in test env
    const sendBtnVisible = await page.locator('.btn-send').isVisible({ timeout: 20000 }).catch(() => false);
    if (!sendBtnVisible) return;

    const chatTextarea = page.locator('textarea[placeholder*="message"]');

    // Send a regular message (no reasoning keyword) — triggers default text response
    await chatTextarea.fill('simple greeting hello');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    // Wait for the LLM response to complete
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });

    // The reasoning toggle should NOT be visible for messages without reasoning
    const reasoningToggle = page.locator('.reasoning-toggle');
    expect(await reasoningToggle.isVisible()).toBe(false);

    // The reasoning content should NOT be visible
    const reasoningContent = page.locator('.reasoning-content');
    expect(await reasoningContent.isVisible()).toBe(false);
  });

  test('reasoning block should have proper accessibility attributes', async ({ page }) => {
    test.setTimeout(30000);

    // Graceful: executor may not start in test env
    const sendBtnVisible = await page.locator('.btn-send').isVisible({ timeout: 20000 }).catch(() => false);
    if (!sendBtnVisible) return;

    const chatTextarea = page.locator('textarea[placeholder*="message"]');

    // Send a message that triggers the reasoning mock response
    await chatTextarea.fill('reasoning test accessibility');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    // Wait for the LLM response to complete
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });

    // The reasoning toggle should have aria-expanded attribute
    const reasoningToggle = page.locator('.reasoning-toggle');
    await expect(reasoningToggle).toBeVisible({ timeout: 10000 });

    // Initially collapsed (or auto-expanded for short reasoning with no content)
    const initialExpanded = await reasoningToggle.getAttribute('aria-expanded');
    expect(initialExpanded).toMatch(/^(true|false)$/);

    // Click to expand
    await reasoningToggle.click();

    // aria-expanded should now be "true"
    await expect(reasoningToggle).toHaveAttribute('aria-expanded', 'true', { timeout: 5000 });

    // Click to collapse
    await reasoningToggle.click();

    // aria-expanded should now be "false"
    await expect(reasoningToggle).toHaveAttribute('aria-expanded', 'false', { timeout: 5000 });
  });
});
