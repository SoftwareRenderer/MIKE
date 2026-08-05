import { test, expect } from '@playwright/test';
import { apiPutConfig, waitForConfig } from './helpers';

test.describe('Debug Cards', () => {
  test.beforeEach(async ({ page }) => {
    // Capture console messages
    page.on('console', msg => {
      if (msg.type() === 'error') {
        console.log(`[BROWSER ERROR] ${msg.text()}`);
      }
    });
    page.on('pageerror', err => {
      console.log(`[PAGE ERROR] ${err.message}`);
    });

    await apiPutConfig(page, {
      providers: [
        {
          name: 'delete-test',
          display_name: 'Delete Test',
          url: 'http://delete-test:9999/v1',
          models: ['zebra', 'alpha', 'mango'],
          concurrency: 1,
        },
      ],
    });
    await waitForConfig(page, c =>
      c.providers && c.providers.length === 1 && c.providers[0].name === 'delete-test'
    );
  });

  test('should show cards after config reset', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('.board')).toBeVisible({ timeout: 10000 });

    // Check board HTML
    const boardHTML = await page.locator('.board').innerHTML();
    console.log('Board HTML (first 2000 chars):', boardHTML.substring(0, 2000));

    // Check columns
    const columns = page.locator('.column');
    console.log('Column count:', await columns.count());

    // Check for card-list
    const cardLists = page.locator('.card-list');
    console.log('Card list count:', await cardLists.count());
    if (await cardLists.count() > 0) {
      console.log('Card list HTML (first 1000 chars):', (await cardLists.first().innerHTML()).substring(0, 1000));
    }

    // Check for .card
    const cards = page.locator('.card');
    console.log('Card count:', await cards.count());

    // Check API directly
    const tasks = await page.evaluate(async () => {
      const res = await fetch('/api/projects');
      const projects = await res.json();
      if (!projects.length) return 'no projects';
      const boardRes = await fetch(`/api/projects/${projects[0].id}/board`);
      const board = await boardRes.json();
      return JSON.stringify({ taskCount: board.tasks?.length, tasks: board.tasks?.slice(0, 2) });
    });
    console.log('API tasks:', tasks);
  });
});
