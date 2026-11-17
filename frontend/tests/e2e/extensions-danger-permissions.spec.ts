import { test, expect } from '@playwright/test';
import { identityKey } from '../../src/lib/utils/extension-identity';

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
 * The card is addressed by `data-identity`, the author+name key the app itself compares on — a display
 * name is not unique in an Installed list that also holds whatever a developer's own `extensions/`
 * directory carries.
 *
 * Runs in the `isolated` project: installing an extension modifies server state, and `html-preview.spec.ts`
 * installs the same extension on the shared lane.
 */
test.describe.serial('Dangerous permission warnings in Extensions tab', () => {
  test('flags sandbox-escaping permissions with a warning icon, sorted first', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });

    // Install the local html-preview extension (UI type — no image build). The author it installed
    // under is the identity the Installed card must then be found by.
    const res = await page.evaluate(async () => {
      const session = await (await fetch('/api/auth/session')).json();
      const csrf = session.csrf_token || '';
      const localRes = await fetch('/api/extensions/local');
      const local = await localRes.json();
      const entry = local.find((e: any) => e.name === 'html-preview');
      if (!entry) return { status: 404, text: 'html-preview not in local extensions list', author: '' };
      const r = await fetch('/api/extensions/install-local', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf },
        credentials: 'include',
        body: JSON.stringify({ author: entry.author, name: entry.name }),
      });
      const text = await r.text(); // SSE stream; fetch resolves when install completes
      return { status: r.status, text, author: entry.author };
    });
    // "already installed" means the state this test needs is already there (a prior run's residue);
    // treating it as a failure would couple the spec to that residue instead of asserting the state.
    if (res.status !== 200 && res.status !== 201 && !(res.status === 409 && res.text.includes('already installed'))) {
      throw new Error(`install-local failed (${res.status}): ${res.text?.slice(0, 300)}`);
    }

    // Open Settings → Extensions (Installed view) — fresh mount loads the list.
    await page.click('.icon-btn[aria-label="Settings"]');
    await page.waitForSelector('[role="dialog"]', { timeout: 5000 });
    await page.click('text=Extensions');
    await page.waitForSelector('.extensions-tab', { timeout: 5000 });

    // Open the HTML Preview card's capability details.
    const card = page.locator(`.extension-card[data-identity="${identityKey(res.author, 'html-preview')}"]`);
    await expect(card).toBeVisible({ timeout: 10000 });
    await card.locator('.cap-badge').first().click();
    await expect(page.locator('.caps-list')).toBeVisible({ timeout: 5000 });

    // The two sandbox-escaping permissions sort to the top, each with a
    // warning icon right after its label; the safe permissions follow in
    // manifest order without an icon. (The icon's <title> text is part of the
    // rendered text content, so normalize before comparing.)
    const norm = (s: string) => s.replace(/\s+/g, ' ').trim();
    const names = page.locator('.cap-permission-name');
    await expect(names).toHaveCount(6);
    const labels = (await names.allTextContents()).map(norm);
    expect(labels[0]).toContain('Run scripts in the preview');
    expect(labels[0]).toContain('Potentially dangerous');
    expect(labels[1]).toContain('Send/receive data in the preview');
    expect(labels[1]).toContain('Potentially dangerous');
    expect(labels.slice(2)).toEqual([
      'Add a button to the editor',
      'Replace the editor view',
      'Read the open file',
      'Show a modal',
    ]);
    for (let i = 0; i < 2; i++) {
      expect(await names.nth(i).locator('svg.perm-danger-icon').count()).toBe(1);
    }
    for (let i = 2; i < 6; i++) {
      expect(await names.nth(i).locator('svg.perm-danger-icon').count()).toBe(0);
    }
  });

  // No afterAll uninstall. Deleting a row deletes its directory (`docs/extension-lifecycle.md`), and this
  // lane's directory is the copy global setup seeded from the sibling extensions repo — the source
  // `html-preview.spec.ts` installs from when it runs after this file. It owns that deletion instead.
});
