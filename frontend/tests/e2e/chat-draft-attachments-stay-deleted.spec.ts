import { test, expect, type Page } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, ensureTaskStarted, isolatedProject, clearTasksAfterEach, gotoProject, sendMessage, waitForTurnAnswered, clickCard, readStored } from './helpers';

// A turn runs in this file's tasks, and turns are queued per project: own the project.
isolatedProject();
// Each test starts a turn, and a live task is charged against the executor's memory budget while it exists:
// release them between tests, not only at the end of the file.
clearTasksAfterEach();

test.describe.configure({ mode: 'serial' });

// The attachments store holds what the tray holds, empty included, so an image the person removed has to
// leave the store too — or the next read paints its thumbnail back.

/** A 1x1 PNG: it is inside both image caps, so the tray holds the bytes it was given. */
const PNG = Buffer.from(
    'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==',
    'base64',
);

interface StoredAttachment {
    mimeType: string;
    data: string;
}

function openTaskId(page: Page): string {
    return new URL(page.url()).searchParams.get('task')!;
}

/** What the `attachments` store holds for this task, or null when it holds nothing. */
function storedAttachments(page: Page, taskId: string): Promise<StoredAttachment[] | null> {
    return readStored<StoredAttachment[]>(page, 'attachments', taskId);
}

/** How many thumbnails the composer's tray holds, or `-1` while the form is not on screen. */
function trayThumbs(page: Page): Promise<number> {
    return page.evaluate(() => {
        const form = document.querySelector('.chat-form');
        return form ? form.querySelectorAll('.attachment-thumb').length : -1;
    });
}

async function openTask(page: Page, projectId: string): Promise<void> {
    // Pinned to `text`: the task's own turn has to answer and end before the tray is worked on.
    const taskId = await createTaskViaApi(page, projectId, `deleted-attachment-${Date.now()} llm-fixture:text`);
    await navigateToTask(page, taskId, projectId);
    await ensureTaskStarted(page, projectId, taskId);
    await expect(page.locator('.chat-form')).toBeVisible();
}

/** Picks an image through the attach button and waits for the tray to show it. */
async function attachImage(page: Page): Promise<void> {
    const picked = page.waitForEvent('filechooser');
    await page.locator('.chat-form .btn-attach').click();
    await (await picked).setFiles({ name: 'shot.png', mimeType: 'image/png', buffer: PNG });
    await expect(page.locator('.chat-form .attachment-thumb')).toHaveCount(1);
}

/** Removes it the way a person does: the cross on the thumbnail. */
async function removeImage(page: Page): Promise<void> {
    await page.locator('.chat-form .attachment-remove').first().click();
    await expect(page.locator('.chat-form .attachment-thumb')).toHaveCount(0);
}

test.describe('Draft images the person removed', () => {
    test.beforeEach(async ({ page }) => {
        await gotoProject(page);
        await page.waitForSelector('.project-item');
    });

    test('leaves the store when it leaves the tray', async ({ page }) => {
        const projectId = await getProjectId(page);
        await openTask(page, projectId);
        const taskId = openTaskId(page);

        await attachImage(page);
        await expect.poll(() => storedAttachments(page, taskId),
            { message: 'the attached image reaches the attachments store' })
            .toEqual([{ mimeType: 'image/png', data: PNG.toString('base64') }]);

        await removeImage(page);

        await expect.poll(() => storedAttachments(page, taskId),
            { message: 'an emptied tray empties the attachments store' }).toBeNull();
    });

    test('stays removed across a reload', async ({ page }) => {
        const projectId = await getProjectId(page);
        await openTask(page, projectId);
        const taskId = openTaskId(page);

        await attachImage(page);
        await expect.poll(() => storedAttachments(page, taskId),
            { message: 'the attached image reaches the attachments store' }).not.toBeNull();
        await removeImage(page);

        await page.reload();
        await expect(page.locator('.task-detail-page')).toBeVisible();
        await expect(page.locator('.chat-form')).toBeVisible();
        await expect.poll(() => trayThumbs(page), { message: 'the reloaded tray holds an image nobody re-attached' })
            .toBe(0);
    });

    test('stays removed when the task view is reopened without a reload', async ({ page }) => {
        const projectId = await getProjectId(page);
        await openTask(page, projectId);
        const taskId = openTaskId(page);
        const awayId = await createTaskViaApi(page, projectId, `switch-away-${Date.now()} llm-fixture:text`);
        await ensureTaskStarted(page, projectId, awayId);

        await attachImage(page);
        await expect.poll(() => storedAttachments(page, taskId),
            { message: 'the attached image reaches the attachments store' }).not.toBeNull();
        await removeImage(page);

        // A card click is a client-side switch: no reload, and this task's chat is rebuilt on the way back.
        await clickCard(page, awayId);
        await expect(page.locator('.chat-form')).toBeVisible();
        await clickCard(page, taskId);
        await expect(page.locator('.chat-form')).toBeVisible();

        await expect.poll(() => trayThumbs(page),
            { message: 'the reopened tray holds an image nobody re-attached' }).toBe(0);
    });

    test('does not come back into the tray while the agent answers', async ({ page }) => {
        const projectId = await getProjectId(page);
        await openTask(page, projectId);
        const taskId = openTaskId(page);

        // A turn that streams for a while: the board frames it brings are what make this screen re-read the
        // draft around the removal.
        await sendMessage(page, 'llm-fixture:text_slow');
        await page.waitForSelector('.chat-message');

        await attachImage(page);
        await expect.poll(() => storedAttachments(page, taskId),
            { message: 'the attached image reaches the attachments store' }).not.toBeNull();
        await removeImage(page);

        // Sampled while the turn runs rather than asserted once after it: the reads a turn causes are what
        // put the image back.
        const sampling = (async () => {
            const seen: number[] = [];
            for (let i = 0; i < 40; i++) {
                seen.push(await trayThumbs(page));
                await page.waitForTimeout(200);
            }
            return seen;
        })();
        await waitForTurnAnswered(page, projectId, taskId);
        const samples = await sampling;

        expect(samples.filter(n => n !== 0), 'the agent answered and the tray holds an image again')
            .toEqual([]);
        await expect(page.locator('.chat-form .attachment-thumb')).toHaveCount(0);
    });
});
