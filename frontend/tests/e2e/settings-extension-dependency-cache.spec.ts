import { test, expect } from '@playwright/test';
import fs from 'fs';
import os from 'os';
import path from 'path';

/**
 * Granting a language server its dependency directory, end to end: what the field shows is what lands
 * in the database, a missing directory comes back as a sentence, an empty value revokes, and the
 * running sandbox is stopped so the next use recreates it. Component states are in vitest.
 *
 * Subject: the local fixture `tests/fixtures/local-extensions/testdev/dep-cache-lsp`, which declares
 * `capabilities.lsp.sandbox.dependency_cache`. A local LSP install needs no image, so this is a real
 * install; it is deleted at the end.
 */

const FIXTURE_AUTHOR = 'testdev';
const FIXTURE_NAME = 'dep-cache-lsp';
const DISPLAY = 'Dependency Cache Fixture';

/**
 * A directory that exists, for the grant to point at: the server refuses one that does not. A directory
 * of the test's own under a temp folder, which is what the field asks for — a bare top level of the
 * machine is refused, and whatever is granted is readable only from this extension's own sandbox.
 */
const cacheDir = fs.mkdtempSync(path.join(os.tmpdir(), 'kanban-dep-cache-')) + '/mod';
fs.mkdirSync(cacheDir, { recursive: true });

let installedId: string | null = null;

/** Calls the API with the page's own session and CSRF token, as the UI does. */
async function api(page: import('@playwright/test').Page, method: string, url: string, body?: unknown) {
    return page.evaluate(
        async ({ method, url, body }) => {
            const session = await (await fetch('/api/auth/session')).json();
            const headers: Record<string, string> = { 'Content-Type': 'application/json' };
            if (session.csrf_token) headers['X-CSRF-Token'] = session.csrf_token;
            const res = await fetch(url, {
                method,
                headers,
                credentials: 'include',
                body: body === undefined ? undefined : JSON.stringify(body),
            });
            const text = await res.text();
            let json: any = null;
            try { json = JSON.parse(text); } catch { /* SSE and plain-text answers have no JSON body */ }
            return { status: res.status, json, text };
        },
        { method, url, body },
    );
}

async function openExtensionsTab(page: import('@playwright/test').Page) {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 15000 });
    await page.click('.icon-btn[aria-label="Settings"]');
    await page.waitForSelector('[role="dialog"]', { timeout: 5000 });
    await page.click('text=Extensions');
    await page.waitForSelector('.extensions-tab', { timeout: 5000 });
}

function fixtureRow(page: import('@playwright/test').Page) {
    return page.locator('.extension-card').filter({ hasText: DISPLAY });
}

/**
 * Open the fixture's Edit dialog and return its locator. `[role="dialog"]` is never unique here — the
 * Settings dialog is still open behind this one — so the dialog is found by the section it contains.
 */
async function openEditDialog(page: import('@playwright/test').Page) {
    const row = fixtureRow(page);
    const badge = row.getByRole('button', { name: 'Needs setup' });
    if (await badge.count()) await badge.click();
    else await row.locator('button[title^="Edit"]').first().click();

    const dialog = page.locator('[role="dialog"]').filter({ hasText: 'Dependencies directory' }).last();
    await expect(dialog.locator('#edit-dep-path')).toBeVisible({ timeout: 5000 });
    return dialog;
}

test.describe.serial('Extension dependency directory', () => {
    test('installs the fixture extension that declares a dependency cache', async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.project-item', { timeout: 15000 });

        const local = await api(page, 'GET', '/api/extensions/local');
        const entry = (local.json ?? []).find((e: any) => e.name === FIXTURE_NAME);
        expect(entry, `fixture ${FIXTURE_NAME} is not in the local extensions list`).toBeTruthy();

        // The install answers as an SSE stream; fetch resolves when the stream ends.
        const res = await api(page, 'POST', '/api/extensions/install-local', { author: entry.author, name: entry.name });
        expect([200, 201], `install-local failed: ${res.text.slice(0, 300)}`).toContain(res.status);

        const list = await api(page, 'GET', '/api/extensions');
        const installed = (list.json ?? []).find((e: any) => e.name === FIXTURE_NAME);
        expect(installed, 'the fixture did not appear in the installed list').toBeTruthy();
        installedId = installed.id;

        // The declaration has to be on the stored record, or the UI has nothing to ask about and
        // the endpoint has no location to mount at.
        expect(installed.sandbox_config?.dependency_cache).toEqual({
            container_path: '/fake/mod',
            env: 'FAKE_CACHE',
        });
    });

    test('says on the card that it needs a directory, and refuses a path that is not one', async ({ page }) => {
        await openExtensionsTab(page);

        const row = fixtureRow(page);
        // Said twice, because a dot on an icon is easy to look straight past: the badge beside the name
        // and the dot on the Edit icon are the same state.
        const badge = row.getByRole('button', { name: 'Needs setup' });
        await expect(badge, 'a declarer with no directory is not warned about').toBeVisible({ timeout: 5000 });
        await expect(row.locator('.ext-needs-dot')).toBeVisible();

        await badge.click();
        const dialog = page.locator('[role="dialog"]').filter({ hasText: 'Dependencies directory' }).last();
        // Where it looks inside the sandbox is stated, in the extension's own terms. (`.field-desc`
        // is every field's description, so the assertion names the location element itself.)
        await expect(dialog.locator('.ext-dep-env')).toHaveText('FAKE_CACHE=/fake/mod');
        await expect(dialog.locator('.ext-dep-none')).toBeVisible();

        const input = dialog.locator('#edit-dep-path');
        await input.fill('/definitely/not/here-anywhere');
        await dialog.getByRole('button', { name: 'Save' }).click();

        // The server's sentence reaches the field it is about — "create it first" is the actionable
        // half, and a wrapper message that swallowed it would be a worse error than none. The dialog
        // stays open: the refusal is about one field, and the rest of the dialog is still unsaved.
        await expect(dialog.locator('.ext-dep-error')).toContainText('cannot be read', { timeout: 5000 });
        await expect(dialog.locator('.ext-dep-error')).toContainText('create it first');
        await expect(dialog.locator('.ext-dep-path')).toHaveCount(0);
        await expect(fixtureRow(page).getByRole('button', { name: 'Needs setup' })).toBeVisible();
    });

    test('grants the path in the dialog, and it survives a reload', async ({ page }) => {
        await openExtensionsTab(page);
        const dialog = await openEditDialog(page);

        const input = dialog.locator('#edit-dep-path');
        await input.fill(cacheDir);
        // The field does not apply on its own: what grants is the dialog's Save, because applying a
        // grant stops the running sandbox.
        await expect(dialog.locator('.ext-dep-pending')).toContainText('when you save');
        await dialog.getByRole('button', { name: 'Save' }).click();

        await expect(dialog).toHaveCount(0);
        await expect(fixtureRow(page).getByRole('button', { name: 'Needs setup' })).toHaveCount(0);
        await expect(fixtureRow(page).locator('.ext-needs-dot')).toHaveCount(0);

        // A second mount reads it from the server: this is stored state, not a value the component
        // remembered — and the dialog opens showing the directory, not the suggestion.
        await openExtensionsTab(page);
        const reopened = await openEditDialog(page);
        await expect(reopened.locator('.ext-dep-path')).toHaveText(cacheDir, { timeout: 5000 });
        await expect(reopened.locator('#edit-dep-path')).toHaveValue(cacheDir);

        // And the record itself says so, which is what the sandbox builder will read next time.
        const list = await api(page, 'GET', '/api/extensions');
        const mine = (list.json ?? []).find((e: any) => e.id === installedId);
        expect(mine.sandbox_config?.dependency_host_path).toBe(cacheDir);
    });

    test('revokes by clearing the field and saving, which is a decision not an omission', async ({ page }) => {
        await openExtensionsTab(page);
        const dialog = await openEditDialog(page);

        await expect(dialog.locator('.ext-dep-path')).toHaveText(cacheDir, { timeout: 5000 });
        await dialog.getByRole('button', { name: 'Unmount' }).click();
        await dialog.getByRole('button', { name: 'Save' }).click();

        await expect(fixtureRow(page).getByRole('button', { name: 'Needs setup' })).toBeVisible({ timeout: 5000 });

        const list = await api(page, 'GET', '/api/extensions');
        const mine = (list.json ?? []).find((e: any) => e.id === installedId);
        expect(mine.sandbox_config?.dependency_host_path || '').toBe('');
    });

    test('removes the fixture again', async ({ page }) => {
        expect(installedId, 'the install test did not record an id to clean up').toBeTruthy();
        // The cleanup calls the API through the page, and a relative fetch needs a document — this
        // test navigates no further than that.
        await page.goto('/');
        await page.waitForSelector('.project-item', { timeout: 15000 });
        const res = await api(page, 'DELETE', `/api/extensions/${installedId}`);
        expect(res.status, `delete failed: ${res.text.slice(0, 200)}`).toBe(200);

        const list = await api(page, 'GET', '/api/extensions');
        expect((list.json ?? []).some((e: any) => e.id === installedId)).toBe(false);
    });
});

test.afterAll(() => {
    fs.rmSync(cacheDir, { recursive: true, force: true });
});
