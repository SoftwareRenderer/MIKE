import { test, expect } from '@playwright/test';
import { getProjectId, csrfFetch } from './helpers';
import { execSync } from 'node:child_process';

/** Run a command on the host and return stdout. */
function hostShell(cmd: string): string {
    return execSync(cmd, { encoding: 'utf-8', stdio: ['pipe', 'pipe', 'ignore'] }).trim();
}

/** Check if a podman container with the given name pattern is running. */
function isContainerRunning(projectId: string): boolean {
    try {
        const output = hostShell('podman ps --filter name=kanban-sandbox --format "{{.Names}}"');
        return output.split('\n').some(name => name.includes(`proj-${projectId}`));
    } catch {
        return false;
    }
}

test.describe.serial('Sandbox Project Terminal Container Lifecycle', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should stop container when user types exit in project terminal', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Ensure sandbox is enabled
    await csrfFetch(page, `/api/projects/${projectId}`, { method: 'PUT', body: { sandbox_enabled: true } });
    await page.reload();
    await page.waitForSelector('.project-item', { timeout: 10000 });

    // Ensure no task is selected (project-level terminal requires no task selected)
    // The TopBar terminal button is only visible when no task is open
    const url = page.url();
    if (url.includes('task=')) {
        await page.goto('/');
        await page.waitForSelector('.project-item', { timeout: 10000 });
    }

    // Click the terminal button in the TopBar
    await page.click('button[aria-label="Open terminal"]');
    await expect(page.locator('.terminal-panel')).toBeVisible({ timeout: 30000 });

    // Wait for the terminal to be ready (shell prompt appears)
    const terminalContainer = page.locator('.terminal-container');
    await expect(async () => {
      const text = await terminalContainer.textContent();
      expect(text).not.toContain('Starting sandbox container');
    }).toPass({ timeout: 30000 });

    // Verify container is running
    expect(isContainerRunning(projectId)).toBe(true);

    // Type "exit" in the terminal
    const terminalElement = page.locator('.terminal .xterm-textarea').first();
    await terminalElement.click();
    await terminalElement.fill('');
    await terminalElement.type('exit', { delay: 50 });
    await terminalElement.press('Enter');

    // Wait for the terminal panel to close (shell exits → "closed" message → onClosed → close())
    await expect(page.locator('.terminal-panel')).not.toBeVisible({ timeout: 15000 });

    // Wait a moment for the container to be stopped
    await page.waitForTimeout(2000);

    // Verify container was stopped
    expect(isContainerRunning(projectId)).toBe(false);
  });

  test('should create new container on reopen after exit', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Ensure sandbox is enabled
    await csrfFetch(page, `/api/projects/${projectId}`, { method: 'PUT', body: { sandbox_enabled: true } });
    await page.reload();
    await page.waitForSelector('.project-item', { timeout: 10000 });

    // Ensure no task is selected
    const url = page.url();
    if (url.includes('task=')) {
        await page.goto('/');
        await page.waitForSelector('.project-item', { timeout: 10000 });
    }

    // Open terminal, type exit, wait for close
    await page.click('button[aria-label="Open terminal"]');
    await expect(page.locator('.terminal-panel')).toBeVisible({ timeout: 30000 });

    const terminalContainer = page.locator('.terminal-container');
    await expect(async () => {
      const text = await terminalContainer.textContent();
      expect(text).not.toContain('Starting sandbox container');
    }).toPass({ timeout: 30000 });

    // Type exit
    const terminalElement = page.locator('.terminal .xterm-textarea').first();
    await terminalElement.click();
    await terminalElement.fill('');
    await terminalElement.type('exit', { delay: 50 });
    await terminalElement.press('Enter');
    await expect(page.locator('.terminal-panel')).not.toBeVisible({ timeout: 15000 });
    await page.waitForTimeout(2000);

    // Verify container is stopped
    expect(isContainerRunning(projectId)).toBe(false);

    // Reopen terminal
    await page.click('button[aria-label="Open terminal"]');
    await expect(page.locator('.terminal-panel')).toBeVisible({ timeout: 30000 });

    // Wait for new container to start
    await expect(async () => {
      const text = await terminalContainer.textContent();
      expect(text).not.toContain('Starting sandbox container');
    }).toPass({ timeout: 30000 });

    // Verify new container is running
    expect(isContainerRunning(projectId)).toBe(true);

    // Clean up: close terminal
    await page.locator('.terminal-panel .close-btn').click();
    await expect(page.locator('.terminal-panel')).not.toBeVisible({ timeout: 5000 });
  });
});
