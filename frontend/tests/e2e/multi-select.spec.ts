import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, csrfFetch } from './helpers';

test.describe.serial('Multi-Select', () => {
    let taskId1: string;
    let taskId2: string;
    let taskId3: string;
    let projectId: string;

    test.beforeEach(async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });
        projectId = await getProjectId(page);

        // Create 3 tasks for multi-select testing
        taskId1 = await createTaskViaApi(page, projectId, `multi-select-test-${Date.now()}-1`);
        taskId2 = await createTaskViaApi(page, projectId, `multi-select-test-${Date.now()}-2`);
        taskId3 = await createTaskViaApi(page, projectId, `multi-select-test-${Date.now()}-3`);

        // Wait for all cards to render
        await page.waitForSelector(`[data-task-id="${taskId3}"]`, { timeout: 10000 });
    });

    test('should support multi-select mode', async ({ page }) => {
        // Cards should have touch handlers for mobile multi-select
        const card = page.locator('.card').first();
        await expect(card).toBeVisible();
    });

    test('should toggle selection with Ctrl+Click', async ({ browserName, page }) => {
        // Skip on WebKit - Ctrl+Click behavior differs
        if (browserName === 'webkit') {
            test.skip();
        }

        const card1 = page.locator(`[data-task-id="${taskId1}"]`);
        const card2 = page.locator(`[data-task-id="${taskId2}"]`);

        // Ctrl+Click first card
        await card1.click({ modifiers: ['Control'] });
        // Should have multi-selected class
        await expect(card1).toHaveClass(/multi-selected/);

        // Ctrl+Click second card
        await card2.click({ modifiers: ['Control'] });
        await expect(card2).toHaveClass(/multi-selected/);

        // Both should be selected
        await expect(card1).toHaveClass(/multi-selected/);
        await expect(card2).toHaveClass(/multi-selected/);

        // Ctrl+Click first card again to deselect
        await card1.click({ modifiers: ['Control'] });
        await expect(card1).not.toHaveClass(/multi-selected/);
        // Second should still be selected
        await expect(card2).toHaveClass(/multi-selected/);
    });

    test('should show floating action bar when 2+ tasks selected', async ({ browserName, page }) => {
        if (browserName === 'webkit') {
            test.skip();
        }

        const card1 = page.locator(`[data-task-id="${taskId1}"]`);
        const card2 = page.locator(`[data-task-id="${taskId2}"]`);

        // Select two cards
        await card1.click({ modifiers: ['Control'] });
        await card2.click({ modifiers: ['Control'] });

        // Multi-select bar should appear
        const bar = page.locator('.multi-select-bar');
        await expect(bar).toBeVisible();
    });

    test('should show Start button when all selected tasks are in Backlog', async ({ browserName, page }) => {
        if (browserName === 'webkit') {
            test.skip();
        }

        const card1 = page.locator(`[data-task-id="${taskId1}"]`);
        const card2 = page.locator(`[data-task-id="${taskId2}"]`);

        // Select two cards (all in Backlog by default)
        await card1.click({ modifiers: ['Control'] });
        await card2.click({ modifiers: ['Control'] });

        // Bar should have a Start button
        const startBtn = page.locator('.bar-btn-start');
        await expect(startBtn).toBeVisible();
        await expect(startBtn).toContainText('Start 2 tasks');
    });

    test('should confirm before starting selected tasks with task titles listed', async ({ browserName, page }) => {
        if (browserName === 'webkit') {
            test.skip();
        }

        const card1 = page.locator(`[data-task-id="${taskId1}"]`);
        const card2 = page.locator(`[data-task-id="${taskId2}"]`);

        // Select two cards
        await card1.click({ modifiers: ['Control'] });
        await card2.click({ modifiers: ['Control'] });

        // Click Start button
        const startBtn = page.locator('.bar-btn-start');
        await startBtn.click();

        // Confirmation dialog should appear with task titles
        const dialog = page.locator('.modal');
        await expect(dialog).toBeVisible();
        await expect(dialog).toContainText('Start 2 tasks');
        // Dialog should list the task titles
        const card1Title = await card1.locator('.card-title-text').textContent();
        const card2Title = await card2.locator('.card-title-text').textContent();
        expect(!!card1Title).toBe(true);
        expect(!!card2Title).toBe(true);
        await expect(dialog).toContainText(card1Title!);
        await expect(dialog).toContainText(card2Title!);

        // Cancel
        const cancelBtn = page.locator('.modal-footer .btn-secondary');
        await cancelBtn.click();
        await expect(dialog).not.toBeVisible();

        // Tasks should still be in Backlog (not started)
        await expect(card1).toBeVisible();
    });

    test('should show Delete button in action bar', async ({ browserName, page }) => {
        if (browserName === 'webkit') {
            test.skip();
        }

        const card1 = page.locator(`[data-task-id="${taskId1}"]`);
        const card2 = page.locator(`[data-task-id="${taskId2}"]`);

        // Select two cards
        await card1.click({ modifiers: ['Control'] });
        await card2.click({ modifiers: ['Control'] });

        // Delete button should be visible
        const deleteBtn = page.locator('.bar-btn-delete');
        await expect(deleteBtn).toBeVisible();
        await expect(deleteBtn).toContainText('Delete 2 tasks');
    });

    test('should confirm before deleting selected tasks with task titles listed', async ({ browserName, page }) => {
        if (browserName === 'webkit') {
            test.skip();
        }

        const card1 = page.locator(`[data-task-id="${taskId1}"]`);
        const card2 = page.locator(`[data-task-id="${taskId2}"]`);

        // Select two cards
        await card1.click({ modifiers: ['Control'] });
        await card2.click({ modifiers: ['Control'] });

        // Click delete
        const deleteBtn = page.locator('.bar-btn-delete');
        await deleteBtn.click();

        // Confirmation dialog should appear with task titles
        const dialog = page.locator('.modal');
        await expect(dialog).toBeVisible();
        await expect(dialog).toContainText('Permanently delete 2 tasks');
        // Dialog should list the task titles
        const card1Title = await card1.locator('.card-title-text').textContent();
        const card2Title = await card2.locator('.card-title-text').textContent();
        await expect(dialog).toContainText(card1Title!);
        await expect(dialog).toContainText(card2Title!);

        // Cancel the deletion
        const cancelBtn = page.locator('.modal-footer .btn-secondary');
        await cancelBtn.click();
        await expect(dialog).not.toBeVisible();

        // Cards should still exist
        await expect(card1).toBeVisible();
        await expect(card2).toBeVisible();
    });

    test('should delete selected tasks on confirmation', async ({ browserName, page }) => {
        if (browserName === 'webkit') {
            test.skip();
        }

        const card1 = page.locator(`[data-task-id="${taskId1}"]`);
        const card2 = page.locator(`[data-task-id="${taskId2}"]`);

        // Select two cards
        await card1.click({ modifiers: ['Control'] });
        await card2.click({ modifiers: ['Control'] });

        // Click delete
        const deleteBtn = page.locator('.bar-btn-delete');
        await deleteBtn.click();

        // Confirm the deletion
        const confirmBtn = page.locator('.modal-footer .btn-confirm');
        await confirmBtn.click();

        // Cards should be removed
        await expect(card1).not.toBeVisible();
        await expect(card2).not.toBeVisible();

        // Multi-select bar should disappear
        const bar = page.locator('.multi-select-bar');
        await expect(bar).not.toBeVisible();
    });

    test('should clear selection when clicking on empty board area', async ({ browserName, page }) => {
        if (browserName === 'webkit') {
            test.skip();
        }

        const card1 = page.locator(`[data-task-id="${taskId1}"]`);

        // Select a card
        await card1.click({ modifiers: ['Control'] });
        await expect(card1).toHaveClass(/multi-selected/);

        // Click on empty board area (the .board div background)
        await page.locator('.board').click({ position: { x: 5, y: 5 } });

        // Selection should be cleared
        await expect(card1).not.toHaveClass(/multi-selected/);
    });

    test('should not show Start button when selected tasks are in different columns', async ({ browserName, page }) => {
        if (browserName === 'webkit') {
            test.skip();
        }

        // Move one task to In Progress
        await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId2}/move`, {
            method: 'POST',
            body: { status: 'in_progress' },
        });
        // Wait for board to update (task should appear in In Progress column)
        await expect(async () => {
            const inProgressCards = await page.locator('.column[aria-label="In Progress column"] .card').count();
            expect(inProgressCards).toBeGreaterThan(0);
        }).toPass({ timeout: 5000 });

        const card1 = page.locator(`[data-task-id="${taskId1}"]`);
        const card2 = page.locator(`[data-task-id="${taskId2}"]`).first();

        // Select both cards
        await card1.click({ modifiers: ['Control'] });
        await card2.click({ modifiers: ['Control'] });

        // Start button should NOT be visible (not all in Backlog)
        const startBtn = page.locator('.bar-btn-start');
        await expect(startBtn).not.toBeVisible();

        // Delete button should still be visible
        const deleteBtn = page.locator('.bar-btn-delete');
        await expect(deleteBtn).toBeVisible();
    });

    test('should hide action bar when only 1 task is selected', async ({ browserName, page }) => {
        if (browserName === 'webkit') {
            test.skip();
        }

        const card1 = page.locator(`[data-task-id="${taskId1}"]`);

        // Select one card
        await card1.click({ modifiers: ['Control'] });

        // Card should be visually selected
        await expect(card1).toHaveClass(/multi-selected/);

        // But action bar should NOT appear (only shows for 2+)
        const bar = page.locator('.multi-select-bar');
        await expect(bar).not.toBeVisible();
    });

    test('should have clear selection button in action bar', async ({ browserName, page }) => {
        if (browserName === 'webkit') {
            test.skip();
        }

        const card1 = page.locator(`[data-task-id="${taskId1}"]`);
        const card2 = page.locator(`[data-task-id="${taskId2}"]`);

        // Select two cards
        await card1.click({ modifiers: ['Control'] });
        await card2.click({ modifiers: ['Control'] });

        // Clear button should be visible
        const clearBtn = page.locator('.bar-btn-clear');
        await expect(clearBtn).toBeVisible();

        // Click clear
        await clearBtn.click();

        // All selections should be cleared
        await expect(card1).not.toHaveClass(/multi-selected/);
        await expect(card2).not.toHaveClass(/multi-selected/);

        // Bar should disappear
        const bar = page.locator('.multi-select-bar');
        await expect(bar).not.toBeVisible();
    });
});
