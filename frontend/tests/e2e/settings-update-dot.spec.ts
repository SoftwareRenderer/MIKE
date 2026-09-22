import { test, expect, type Page } from '@playwright/test';

/**
 * The release pass runs once per boot (`updateCheck.start()`, called from `loadAppData` so both boot
 * paths reach it), and the dot on the Settings control is what it announces. `docs/auto-update.md`
 * promises the click that follows a mark opens Settings **on About**, where the row explaining the mark
 * lives — which means the tab Settings opens is the app's decision and never a spec's assumption. That
 * is what `settings-provider.spec.ts` assumed, and it timed out on a Providers locator with the About
 * panel on screen.
 *
 * The run's own servers answer `disabled` (`MIKE_DISABLE_UPDATES=1`, global-setup), so the status is
 * stubbed here: the posture a person lives in, without the manifest host in the run. The stub also
 * records the check requests, because which of them carries `?auto=1` is the contract between the daily
 * pass and the install's day. The button's half of that contract — no flag, so the day cannot refuse a
 * person — is pinned by `update-check.unit.svelte.ts` and the handler test instead: About shows the action
 * row only for a saved preference, which a stubbed status cannot grant.
 */

// The shape `/api/updates/status` answers with (backend/updates/service.go). `can_stage` false is what
// keeps the pass's follow-up (`autoPrepare`) from reaching for a download this test never asked for.
const STATUS = {
  current_version: 'v0.0.0-e2e',
  latest: '',
  available: false,
  checked_at: '',
  enabled: false,
  disabled: false,
  answered: true,
  install: { kind: 'packaged', dir: '/opt/mike', can_stage: false },
  manifest_url: 'https://updates.invalid/version',
};

const SETTINGS_BTN = '.icon-btn[aria-label="Settings"]';
const CONSENT_DIALOG = '[role="dialog"]';
const tab = (name: string) => `button.tab-btn:has-text("${name}")`;

interface Stubbed {
  /** Change what the status route answers from now on. */
  setStatus(next: Record<string, unknown>): void;
  /** `pathname?search` of every check request the browser sent, in order. */
  checks: string[];
}

/** Answer the update routes from a status the test can change mid-flight, then load the board. */
async function serveStatus(page: Page, initial: Record<string, unknown>): Promise<Stubbed> {
  let status = { ...STATUS, ...initial };
  const checks: string[] = [];
  await page.route('**/api/updates/status', route => route.fulfill({ json: status }));
  await page.route('**/api/updates/check*', route => {
    const url = new URL(route.request().url());
    checks.push(`${url.pathname}${url.search}`);
    return route.fulfill({ json: status });
  });
  await page.goto('/');
  await page.waitForSelector('.project-item', { timeout: 10000 });
  return { setStatus: next => { status = { ...status, ...next }; }, checks };
}

test('a mark from the boot pass sends the Settings click to the row that explains it', async ({ page }) => {
  const { checks } = await serveStatus(page, {
    enabled: true, available: true, latest: 'v9.9.9', published: '2026-09-20T00:00:00Z',
  });

  // No click yet: the boot pass read the status, and a release with an archive for this machine is lit.
  await expect(page.locator(`${SETTINGS_BTN} [data-update-dot="update"]`)).toBeVisible();
  // The pass asked for the install's own day; a spent day comes back as this same status.
  await expect.poll(() => checks).toEqual(['/api/updates/check?auto=1']);

  await page.locator(SETTINGS_BTN).click();
  await expect(page.locator(tab('About'))).toHaveClass(/active/);
  await expect(page.locator('.about-header')).toBeVisible();
});

test('with nothing to announce the Settings control opens the tab it always did', async ({ page }) => {
  await serveStatus(page, {});

  await expect(page.locator(`${SETTINGS_BTN} [data-update-dot]`)).toHaveCount(0);

  await page.locator(SETTINGS_BTN).click();
  await expect(page.locator(tab('Providers'))).toHaveClass(/active/);
  await expect(page.locator('.about-header')).toHaveCount(0);
});

test('the opt-in question is asked at boot, and answering it is what ends the mark', async ({ page }) => {
  const { setStatus } = await serveStatus(page, { answered: false });

  // "not yet asked" is not "no": a browser that never answered is asked, once, on a settled screen.
  const consent = page.locator(CONSENT_DIALOG).filter({ hasText: 'Enable automatic updates?' });
  await expect(consent).toBeVisible();

  await consent.locator('button:has-text("No")').click();
  // The answer is a preference write, and the pass re-reads the status behind it.
  await page.waitForResponse(r => r.url().includes('/api/config') && r.request().method() === 'PUT');
  setStatus({ answered: true, enabled: false });

  await expect(consent).toHaveCount(0);
  await expect(page.locator(`${SETTINGS_BTN} [data-update-dot]`)).toHaveCount(0);

  await page.locator(SETTINGS_BTN).click();
  await expect(page.locator(tab('Providers'))).toHaveClass(/active/);
});
