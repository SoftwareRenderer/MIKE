import { test, expect, type Page } from '@playwright/test';
import {
    getProjectId,
    createTaskViaApi,
    navigateToTask,
    gotoProject,
    isolatedProject,
    clearTasksAfterEach,
    messageRows,
} from './helpers';

// The edit writes to a task, and a created task is a card on the board: own the project, release the task.
isolatedProject();
clearTasksAfterEach();

// A 1×1 PNG, inside both intake caps, so the browser keeps the bytes it arrived as and the row holds a
// picture rather than a file path.
const PNG = Buffer.from(
    'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==',
    'base64',
);

const EDIT_ACTION = 'button[aria-label="Start new thread from this message"]';

/** Open the editor of the task's question. */
async function editQuestion(page: Page): Promise<void> {
    await page.locator('.chat-message.user').first().locator(EDIT_ACTION).click();
    await expect(page.locator('.edit-form textarea')).toBeVisible();
}

/** Pick a picture into the open editor and see it in the tray. */
async function attachPicture(page: Page): Promise<void> {
    const chooser = page.waitForEvent('filechooser');
    await page.locator('.edit-form .btn-edit-attach').click();
    await (await chooser).setFiles({ name: 'shot.png', mimeType: 'image/png', buffer: PNG });
    await expect(page.locator('.edit-form .edit-attachment-chip img'))
        .toHaveAttribute('src', /^data:image\/png;base64,/);
}

/** Save the open editor and wait for the row to come back. */
async function save(page: Page): Promise<void> {
    await page.locator('.edit-form .btn-edit-submit').click();
    await expect(page.locator('.edit-form')).toHaveCount(0);
}

/** The tray the stored row carries. Rows are read by role, never counted: the window slides. */
async function storedTray(page: Page, projectId: string, taskId: string): Promise<unknown[]> {
    const rows = await messageRows(page, projectId, taskId);
    const row = rows.find(r => r.role === 'user');
    expect(row, 'the task has a question to edit').toBeTruthy();
    const held = (row as { attachments?: string | unknown[] }).attachments;
    if (!held) return [];
    return typeof held === 'string' ? JSON.parse(held) : held;
}

/** A backlog task whose question is on screen. */
async function openQuestion(page: Page, label: string): Promise<{ projectId: string; taskId: string }> {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `${label} ${Date.now()}`);
    await navigateToTask(page, taskId, projectId);
    await expect(page.locator('.chat-message.user')).toHaveCount(1);
    return { projectId, taskId };
}

test.describe('An image added while editing a message', () => {
    test.beforeEach(async ({ page }) => {
        await gotoProject(page);
        await page.waitForSelector('.project-item');
    });

    test('stays on the row after the save', async ({ page }) => {
        const { projectId, taskId } = await openQuestion(page, 'pasted-shot');

        await editQuestion(page);
        await attachPicture(page);
        await save(page);

        await expect(page.locator('.chat-message.user .message-images img')).toBeVisible();
        await expect.poll(() => storedTray(page, projectId, taskId), {
            message: 'the row itself carries the picture, not just the screen',
        }).toHaveLength(1);
    });

    test('leaves the row when it is taken off and saved', async ({ page }) => {
        const { projectId, taskId } = await openQuestion(page, 'shot-removed');

        await editQuestion(page);
        await attachPicture(page);
        await save(page);
        await expect(page.locator('.chat-message.user .message-images img')).toBeVisible();
        await expect.poll(() => storedTray(page, projectId, taskId), {
            message: 'the picture the second edit takes off has to have been stored first',
        }).toHaveLength(1);

        await editQuestion(page);
        await page.locator('.edit-form .remove-attachment').click();
        await expect(page.locator('.edit-form .edit-attachment-chip')).toHaveCount(0);
        await save(page);

        await expect(page.locator('.chat-message.user .message-images')).toHaveCount(0);
        await expect.poll(() => storedTray(page, projectId, taskId), {
            message: 'an emptied tray is a decision the row keeps',
        }).toHaveLength(0);
    });
});
