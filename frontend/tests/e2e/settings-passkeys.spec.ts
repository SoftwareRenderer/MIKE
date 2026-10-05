import { test, expect, type Page } from '@playwright/test';
import { attachVirtualAuthenticator, laneOrigin, mintPairingCode, pairBrowser, signedOutContext } from '../helpers-auth.mjs';

/**
 * Settings → About → Devices & passkeys: the list of credentials this install will sign in, the label
 * a person gives each one, the code that adds one more, and the revocation that takes one away.
 *
 * Mutates immediately — it is outside the modal's Save/Cancel flow — so every assertion here is about
 * what the server now holds, read back after a reload rather than from what the page just rendered.
 *
 * Runs in the `isolated` project: it registers and revokes credentials on the lane server.
 */
const SETTINGS_BTN = '.icon-btn[aria-label="Settings"]';
const ABOUT_TAB = 'button.tab-btn:has-text("About")';
const SECTION = '.passkeys-section';
const ROW = '.passkeys-section .settings-row';
const NAME = '.passkeys-name';
const NAME_INPUT = '.passkeys-name-input';
const REVOKE = '.passkey-revoke';
const MINT = 'button:has-text("Pair a new device")';
const MINT_NAME = `${SECTION} .settings-create-input`;
const CANCEL_CODES = 'button:has-text("Cancel the code")';
const PAIRING_BOX = '.passkeys-pairing';
const CONFIRM_BTN = '.btn-confirm';

async function openPasskeysSection(page: Page) {
  await page.goto('/');
  await page.locator(SETTINGS_BTN).first().click();
  await page.locator(ABOUT_TAB).click();
  await expect(page.locator(SECTION)).toBeVisible();
  await expect(page.locator(ROW).first()).toBeVisible({ timeout: 10000 });
}

test.describe.configure({ mode: 'serial' });
test.describe('Devices & passkeys', () => {
  test('lists the passkeys this install holds', async ({ page }) => {
    await openPasskeysSection(page);
    const rows = page.locator(ROW);
    expect(await rows.count()).toBeGreaterThan(0);
    // A row says what the device is and when it joined; it never shows a credential.
    await expect(rows.first().locator(NAME)).not.toHaveText('');
    await expect(rows.first().getByText('Added')).toBeVisible();
    expect(await page.locator(`${SECTION} code`).count()).toBe(0);
  });

  test('renames a device, and the label survives a reload', async ({ page }) => {
    await openPasskeysSection(page);
    const name = `Probe laptop ${Date.now()}`;
    await page.locator(ROW).first().locator(NAME).click();
    await expect(page.locator(NAME_INPUT)).toBeVisible();
    await page.fill(NAME_INPUT, name);
    await page.keyboard.press('Enter');
    await expect(page.locator(ROW).first().locator(NAME)).toHaveText(name);

    await page.reload();
    await openPasskeysSection(page);
    await expect(page.locator(`${ROW}:has-text(${JSON.stringify(name)})`)).toBeVisible({ timeout: 10000 });
  });

  test('pairing a new device shows a code, a countdown and a QR, and the code can be cancelled', async ({ page }) => {
    await openPasskeysSection(page);
    await page.locator(MINT).click();
    await expect(page.locator(PAIRING_BOX)).toBeVisible();

    // The code is two groups of four, the shape a person reads aloud.
    await expect(page.locator('.passkeys-code')).toHaveText(/^[0-9A-Z]{4}-[0-9A-Z]{4}$/);
    // The picture is the same link, for a device that can scan rather than type.
    await expect(page.locator('img.passkeys-qr')).toHaveAttribute('src', /^data:image\/png;base64,/);
    await expect(page.locator('.passkeys-pairing-countdown')).toContainText('expires in');
    await expect(page.locator('.passkeys-url')).not.toBeEmpty();

    await page.locator(CANCEL_CODES).click();
    await page.locator(CONFIRM_BTN).click();
    await expect(page.locator(PAIRING_BOX)).toHaveCount(0);
  });

  test('a device paired from a minted code appears, and can be revoked again', async ({ page, browser }) => {
    await openPasskeysSection(page);
    const before = await page.locator(ROW).count();

    const base = laneOrigin();
    const pairing = await mintPairingCode(page, base);
    const context = await signedOutContext(browser);
    try {
      const device = await context.newPage();
      await attachVirtualAuthenticator(context, device);
      await pairBrowser(device, base, pairing.code);
    } finally {
      await context.close();
    }

    await page.reload();
    await openPasskeysSection(page);
    // Oldest first, so the device that just joined is the last row.
    await expect(page.locator(ROW)).toHaveCount(before + 1, { timeout: 10000 });

    await page.locator(ROW).last().locator(REVOKE).click();
    await page.locator(CONFIRM_BTN).click();
    await expect(page.locator(ROW)).toHaveCount(before, { timeout: 10000 });
  });

  test('a name typed before minting rides the link and names the device that pairs with it', async ({ page, browser }) => {
    const name = `Scanned tablet ${Date.now()}`;
    await openPasskeysSection(page);
    await page.fill(MINT_NAME, name);
    await page.locator(MINT).click();
    await expect(page.locator(PAIRING_BOX)).toBeVisible();

    // The name travels in the link, not in the code; `+` is the fragment's spelling of a space.
    const link = (await page.locator('.passkeys-url').textContent()) ?? '';
    expect(link).toContain(`&name=${name.replace(/ /g, '+')}`);

    const base = laneOrigin();
    const code = (await page.locator('.passkeys-code').textContent()) ?? '';
    const context = await signedOutContext(browser);
    try {
      const device = await context.newPage();
      await attachVirtualAuthenticator(context, device);
      // The link is the only thing that carries the name.
      await pairBrowser(device, base, code, { link: link.trim() });
    } finally {
      await context.close();
    }

    await page.reload();
    await openPasskeysSection(page);
    await expect(page.locator(`${ROW}:has-text(${JSON.stringify(name)})`)).toBeVisible({ timeout: 10000 });
  });
});
