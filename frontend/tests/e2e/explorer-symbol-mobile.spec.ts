import { test, expect } from '@playwright/test';
import { writeFileSync, rmSync, readdirSync, statSync } from 'node:fs';
import { execSync } from 'node:child_process';
import { join } from 'path';
import { getProjectId } from './helpers';

function getConfig() {
  return JSON.parse(execSync('curl -s http://localhost:3000/api/config', { encoding: 'utf-8' }));
}

function findWorktreeDir(): string | null {
  const worktreesDir = getConfig().workspace_dir;
  try {
    const projectEntries = readdirSync(worktreesDir);
    for (const projEntry of projectEntries) {
      const projPath = join(worktreesDir, projEntry);
      if (!statSync(projPath).isDirectory()) continue;
      const taskEntries = readdirSync(projPath);
      for (const taskEntry of taskEntries) {
        const fullPath = join(projPath, taskEntry);
        if (statSync(fullPath).isDirectory()) {
          return fullPath;
        }
      }
    }
  } catch { /* ignore */ }
  return null;
}

test.describe('Explorer Symbol Mobile', () => {
  test.describe.configure({ mode: 'serial' });

  test.beforeEach(async ({ page }) => {
    test.setTimeout(30000);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should switch to editor tab and scroll to symbol on mobile', async ({ page }) => {
    const worktreeDir = findWorktreeDir();
    const projectId = await getProjectId(page);
    if (!worktreeDir || !projectId) {
      await expect(page.locator('.board')).toBeVisible();
      return;
    }

    const uniqueId = Date.now();
    const testFile = `symbol-mobile-${uniqueId}.ts`;

    // Create a TypeScript file with multiple functions at known line numbers
    const fileContent = `// Test file for symbol navigation
import { useState } from 'react';

export function firstFunction() {
  console.log('first');
}

export function secondFunction() {
  console.log('second');
}

export function thirdFunction() {
  console.log('third');
}

export function fourthFunction() {
  console.log('fourth');
}

export function fifthFunction() {
  console.log('fifth');
}
`;
    writeFileSync(join(worktreeDir, testFile), fileContent);

    try {
      // Set mobile viewport
      await page.setViewportSize({ width: 390, height: 844 });

      // Open explorer
      await page.locator('.project-item').first().click();
      await page.waitForSelector('.top-bar', { timeout: 10000 });
      await page.locator('.explorer-btn').click();
      await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
      await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 10000 });

      // Open the test file
      await page.locator('.file-browser-item', { hasText: testFile }).click();
      await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 15000 });

      // Wait for symbols to be extracted (give it a moment for the debounced extraction)
      await page.waitForTimeout(1000);

      // Verify the mobile tab bar is visible
      await expect(page.locator('.mobile-tab-bar')).toBeVisible();

      // Click on the symbols tab
      await page.locator('.tab-btn:has-text("Symbols")').click();

      // Wait for symbols to appear in the outline
      await expect(page.locator('.symbol-item')).toHaveCount(5, { timeout: 10000 });

      // Verify we're on the symbols tab
      const activeTabBtn = page.locator('.tab-btn.active');
      await expect(activeTabBtn).toContainText('Symbols');

      // Click on "thirdFunction" (should be on line 17)
      const thirdSymbol = page.locator('.symbol-item', { hasText: 'thirdFunction' });
      await expect(thirdSymbol).toBeVisible();
      await thirdSymbol.click();

      // Verify the editor tab is now active
      await expect(page.locator('.tab-btn.active')).toContainText(testFile.split('/').pop()!, { timeout: 5000 });

      // Verify the editor is visible
      await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 5000 });

      // Verify the cursor is on the correct line (thirdFunction is on line 17)
      const activeLineGutter = page.locator('.cm-activeLineGutter');
      await expect(activeLineGutter).toBeVisible({ timeout: 5000 });
      const activeLineNumber = await activeLineGutter.textContent();
      expect(activeLineNumber?.trim()).toBe('17');
    } finally {
      // Reset viewport
      await page.setViewportSize({ width: 1280, height: 720 });
      // Clean up
      try {
        rmSync(join(worktreeDir, testFile), { force: true });
      } catch { /* ignore */ }
    }
  });

  test('should switch to editor tab when clicking a heading symbol in markdown on mobile', async ({ page }) => {
    const worktreeDir = findWorktreeDir();
    const projectId = await getProjectId(page);
    if (!worktreeDir || !projectId) {
      await expect(page.locator('.board')).toBeVisible();
      return;
    }

    const uniqueId = Date.now();
    const testFile = `symbol-md-${uniqueId}.md`;

    // Create a markdown file with headings at known line numbers
    const fileContent = `# Introduction

This is the introduction section with some content.

## Getting Started

Follow these steps to get started with the project.

## Configuration

Configure the application using environment variables.

### Database Settings

Set the database connection parameters.

### Cache Settings

Configure the caching layer.

## Deployment

Deploy the application to production.
`;
    writeFileSync(join(worktreeDir, testFile), fileContent);

    try {
      // Set mobile viewport
      await page.setViewportSize({ width: 390, height: 844 });

      // Open explorer
      await page.locator('.project-item').first().click();
      await page.waitForSelector('.top-bar', { timeout: 10000 });
      await page.locator('.explorer-btn').click();
      await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
      await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 10000 });

      // Open the test file
      await page.locator('.file-browser-item', { hasText: testFile }).click();
      await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 15000 });

      // Wait for symbols to be extracted
      await page.waitForTimeout(1000);

      // Verify the mobile tab bar is visible
      await expect(page.locator('.mobile-tab-bar')).toBeVisible();

      // Click on the symbols tab
      await page.locator('.tab-btn:has-text("Symbols")').click();

      // Wait for outline items to appear (markdown headings)
      await expect(async () => {
        const count = await page.locator('.outline-item').count();
        expect(count).toBeGreaterThan(0);
      }).toPass({ timeout: 10000 });

      // Verify we have outline items
      const outlineCount = await page.locator('.outline-item').count();
      expect(outlineCount).toBeGreaterThan(0);

      // Verify we're on the symbols tab
      const activeTabBtn = page.locator('.tab-btn.active');
      await expect(activeTabBtn).toContainText('Symbols');

      // Click on a heading symbol (e.g., "Cache Settings" or any visible one)
      const outlineItems = page.locator('.outline-item .symbol-item');
      const itemCount = await outlineItems.count();
      // Pick an item that's not the first one
      const targetIdx = Math.min(2, itemCount - 1);
      const targetItem = outlineItems.nth(targetIdx);
      await targetItem.click();

      // Verify the editor tab is now active
      await expect(page.locator('.tab-btn.active')).toContainText(testFile.split('/').pop()!, { timeout: 5000 });

      // Verify the editor is visible
      await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 5000 });

      // Verify the cursor moved to a line > 1 (not the first heading)
      const activeLineGutter = page.locator('.cm-activeLineGutter');
      await expect(activeLineGutter).toBeVisible({ timeout: 5000 });
      const activeLineNumber = await activeLineGutter.textContent();
      const lineNum = parseInt(activeLineNumber?.trim() || '0', 10);
      expect(lineNum).toBeGreaterThan(1);
    } finally {
      // Reset viewport
      await page.setViewportSize({ width: 1280, height: 720 });
      // Clean up
      try {
        rmSync(join(worktreeDir, testFile), { force: true });
      } catch { /* ignore */ }
    }
  });
});
