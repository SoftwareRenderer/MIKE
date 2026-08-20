import { test, expect } from '@playwright/test';

/**
 * Dangerous-permission display — Settings → Extensions.
 *
 * Permissions that escape the sandbox (external requests, running a page's
 * scripts, page network egress) must show a warning icon right after their
 * label and sort to the top of the permission list. Uses the local
 * html-preview extension, which declares the two dangerous preview
 * permissions (`preview.serveScripts`, `preview.serveNetwork`) alongside safe
 * editor ones.
 *
 * Runs in the `isolated` project: installing an extension modifies server
 * state, and the main-project html-preview spec installs the same extension
 * concurrently (cross-spec race on the shared DB).
 */
test.describe.serial('Dangerous permission warnings in Extensions tab', () => {
  test('flags sandbox-escaping permissions with a warning icon, sorted first', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });

    // Install the local html-preview extension (UI type — no image build).
    const res = await page.evaluate(async () => {
      const session = await (await fetch('/api/auth/session')).json();
      const csrf = session.csrf_token || '';
      const localRes = await fetch('/api/extensions/local');
      const local = await localRes.json();
      const entry = local.find((e: any) => e.name === 'html-preview');
      if (!entry) return { status: 404, text: 'html-preview not in local extensions list' };
      const r = await fetch('/api/extensions/install-local', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf },
        credentials: 'include',
        body: JSON.stringify({ author: entry.author, name: entry.name }),
      });
      const text = await r.text(); // SSE stream; fetch resolves when install completes
      return { status: r.status, text };
    });
    if (res.status !== 200 && res.status !== 201) {
      throw new Error(`install-local failed (${res.status}): ${(res as any).text?.slice(0, 300)}`);
    }

    // Open Settings → Extensions (Installed view) — fresh mount loads the list.
    await page.click('.icon-btn[aria-label="Settings"]');
    await page.waitForSelector('[role="dialog"]', { timeout: 5000 });
    await page.click('text=Extensions');
    await page.waitForSelector('.extensions-tab', { timeout: 5000 });

    // Open the HTML Preview card's capability details.
    const card = page.locator('.extension-card', { hasText: 'HTML Preview' });
    await expect(card).toBeVisible({ timeout: 10000 });
    await card.locator('.cap-badge[aria-label*="HTML Preview"]').first().click();
    await expect(page.locator('.caps-list')).toBeVisible({ timeout: 5000 });

    // The two sandbox-escaping permissions sort to the top, each with a
    // warning icon right after its label; the safe permissions follow in
    // manifest order without an icon. (The icon's <title> text is part of the
    // rendered text content, so normalize before comparing.)
    const norm = (s: string) => s.replace(/\s+/g, ' ').trim();
    const names = page.locator('.cap-permission-name');
    await expect(names).toHaveCount(5);
    const labels = (await names.allTextContents()).map(norm);
    expect(labels[0]).toContain('Run scripts in the preview');
    expect(labels[0]).toContain('Potentially dangerous');
    expect(labels[1]).toContain('Send/receive data in the preview');
    expect(labels[1]).toContain('Potentially dangerous');
    expect(labels.slice(2)).toEqual([
      'Add a button to the editor',
      'Replace the editor view',
      'Read the open file',
    ]);
    for (let i = 0; i < 2; i++) {
      expect(await names.nth(i).locator('svg.perm-danger-icon').count()).toBe(1);
    }
    for (let i = 2; i < 5; i++) {
      expect(await names.nth(i).locator('svg.perm-danger-icon').count()).toBe(0);
    }
  });
});
