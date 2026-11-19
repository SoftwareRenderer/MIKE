import { test, expect, type Page } from '@playwright/test';
import { deflateSync } from 'node:zlib';
import {
    getProjectId,
    createTaskViaApi,
    navigateToTask,
    ensureTaskStarted,
    isolatedProject,
    clearTasksAfterEach,
    gotoProject,
    readStored,
} from './helpers';

// A created task auto-starts a turn, and turns are queued per project: own the project.
isolatedProject();
// A live task is charged against the executor's memory budget while it exists: release them per test.
clearTasksAfterEach();

// The image is decided from the bytes before anything is stored, so what the attachments store holds is what
// the model is shown.

/** The ceiling `MAX_IMAGE_PIXELS` puts on the picture a provider is shown. */
const MAX_IMAGE_PIXELS = 16_000_000;

interface StoredAttachment {
    mimeType: string;
    data: string;
}

/**
 * A valid grayscale PNG of these dimensions. It is a solid colour, so the file stays small while its pixels
 * cross the ceiling: the rule reads the picture's size, not the file's.
 */
function pngOf(width: number, height: number): Buffer {
    const rows = Buffer.alloc(height * (1 + width)); // one filter byte then one sample per pixel
    return Buffer.concat([
        Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
        pngChunk('IHDR', Buffer.concat([
            Buffer.from([width >>> 24, (width >>> 16) & 0xff, (width >>> 8) & 0xff, width & 0xff]),
            Buffer.from([height >>> 24, (height >>> 16) & 0xff, (height >>> 8) & 0xff, height & 0xff]),
            Buffer.from([8, 0, 0, 0, 0]), // 8-bit grayscale, no interlace
        ])),
        pngChunk('IDAT', deflateSync(rows)),
        pngChunk('IEND', Buffer.alloc(0)),
    ]);
}

function pngChunk(type: string, data: Buffer): Buffer {
    const body = Buffer.concat([Buffer.from(type, 'ascii'), data]);
    const length = Buffer.alloc(4);
    length.writeUInt32BE(data.length);
    const crc = Buffer.alloc(4);
    crc.writeUInt32BE(crc32(body));
    return Buffer.concat([length, body, crc]);
}

let crcTable: Uint32Array | null = null;

function crc32(buf: Buffer): number {
    if (!crcTable) {
        crcTable = new Uint32Array(256);
        for (let n = 0; n < 256; n++) {
            let c = n;
            for (let bit = 0; bit < 8; bit++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
            crcTable[n] = c >>> 0;
        }
    }
    let crc = 0xffffffff;
    for (const byte of buf) crc = crcTable[(crc ^ byte) & 0xff] ^ (crc >>> 8);
    return (crc ^ 0xffffffff) >>> 0;
}

/** What the `attachments` store holds for this task's tray, or null when it holds nothing. */
function storedAttachment(page: Page, taskId: string): Promise<StoredAttachment | null> {
    return readStored<StoredAttachment[]>(page, 'attachments', taskId).then((list) => list?.[0] ?? null);
}

/** The stored bytes measured as a picture, which is how the provider reads them too. */
function measure(page: Page, att: StoredAttachment): Promise<{ width: number; height: number }> {
    return page.evaluate(async (a) => {
        // Decoded here rather than fetched from a data: URL, which the page's own CSP refuses.
        const bytes = Uint8Array.from(atob(a.data), (c) => c.charCodeAt(0));
        const bitmap = await createImageBitmap(new Blob([bytes], { type: a.mimeType }));
        const size = { width: bitmap.width, height: bitmap.height };
        bitmap.close();
        return size;
    }, att);
}

/** A task whose environment is prepared — an upload has a worktree to land in. */
async function openTask(page: Page, projectId: string, label: string): Promise<string> {
    const taskId = await createTaskViaApi(page, projectId, `${label}-${Date.now()} llm-fixture:text`);
    await navigateToTask(page, taskId, projectId);
    await ensureTaskStarted(page, projectId, taskId);
    await expect.poll(async () => page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.ok ? (await res.json()).worktree_exists === true : false;
    }, { pid: projectId, tid: taskId }), { message: 'the task has an environment to upload into' })
        .toBe(true);
    await expect(page.locator('.chat-form')).toBeVisible();
    return taskId;
}

async function pick(page: Page, file: { name: string; mimeType: string; buffer: Buffer }): Promise<void> {
    const chooser = page.waitForEvent('filechooser');
    await page.locator('.chat-form .btn-attach').click();
    await (await chooser).setFiles(file);
}

test.describe('The image a provider is shown', () => {
    test.beforeEach(async ({ page }) => {
        await gotoProject(page);
        await page.waitForSelector('.project-item');
    });

    test('of an 18-megapixel pick fits the ceiling and stands on the pick\'s ratio', async ({ page }) => {
        const projectId = await getProjectId(page);
        const taskId = await openTask(page, projectId, 'wide-shot');

        await pick(page, { name: 'wide.png', mimeType: 'image/png', buffer: pngOf(6000, 3000) });
        await expect(page.locator('.chat-form .attachment-thumb')).toHaveCount(1);

        await expect.poll(() => storedAttachment(page, taskId),
            { message: 'the picked image reaches the attachments store' }).not.toBeNull();
        const att = await storedAttachment(page, taskId);
        expect(att!.mimeType, 'the format the person attached is the format the provider is shown')
            .toBe('image/png');

        const size = await measure(page, att!);
        expect(size.width * size.height).toBeLessThanOrEqual(MAX_IMAGE_PIXELS);
        expect(size.width * size.height).toBeGreaterThan(0.9 * MAX_IMAGE_PIXELS);
        // 6000x3000 is 2:1; flooring each side off one ratio leaves at most a pixel of error on each.
        expect(Math.abs(size.width * 3000 - size.height * 6000)).toBeLessThanOrEqual(9000);
    });

    test('of a pick under both caps is the bytes that were picked', async ({ page }) => {
        const projectId = await getProjectId(page);
        const taskId = await openTask(page, projectId, 'tiny-shot');
        const png = pngOf(8, 6);

        await pick(page, { name: 'tiny.png', mimeType: 'image/png', buffer: png });
        await expect(page.locator('.chat-form .attachment-thumb')).toHaveCount(1);

        // An image that already fits is never resampled, so the store holds the file's own bytes.
        await expect.poll(() => storedAttachment(page, taskId),
            { message: 'the picked image reaches the attachments store unchanged' })
            .toEqual({ mimeType: 'image/png', data: png.toString('base64') });
    });

    test('becomes a file when the bytes are no picture, and says so', async ({ page }) => {
        const projectId = await getProjectId(page);
        const taskId = await openTask(page, projectId, 'not-an-image');
        const broken = Buffer.from('these bytes are typed image/png and are not one', 'utf8');

        await pick(page, { name: 'broken.png', mimeType: 'image/png', buffer: broken });

        await expect(page.locator('.chat-form .attachment-thumb')).toHaveCount(0);
        await expect(page.locator('.toast')).toContainText('attached as a file', { timeout: 10000 });
        // The composer names where the bytes went, as it does for any dropped file.
        await expect(page.locator('.chat-form textarea')).toHaveValue(/\.mike-uploads\/broken\.png/);
        await expect.poll(() => storedAttachment(page, taskId),
            { message: 'a file the tray refused never enters the tray' }).toBeNull();
    });
});
