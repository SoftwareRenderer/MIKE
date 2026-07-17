import { test, expect } from '@playwright/test';

test.describe.configure({ mode: 'parallel' });
test.describe('Login', () => {
    test.beforeEach(async ({ page }) => {
        // Clear cookies to simulate fresh session
        const context = page.context();
        await context.clearCookies();
        await page.goto('/');
    });

    test('should show login modal when not authenticated', async ({ page }) => {
        await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 10000 });
        await expect(page.locator('[role="dialog"] h2')).toHaveText('Sign in');
    });

    test('should hide app content when not authenticated', async ({ page }) => {
        // The sidebar and main content should not be visible
        await expect(page.locator('.sidebar')).not.toBeVisible();
        await expect(page.locator('.project-item')).not.toBeVisible();
    });

    test('should login with correct password', async ({ page }) => {
        await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 10000 });

        // Login via API directly (bypass UI for reliability)
        const context = page.context();
        const response = await context.request.post('/api/auth/login', {
            data: { password: 'test-password-1234' },
        });
        expect(response.status()).toBe(200);

        // Verify CSRF token is present in response headers
        const csrfToken = response.headers()['x-csrf-token'];
        expect(csrfToken).toBeTruthy();
        expect(csrfToken!.length).toBe(32);

        // Reload page to reflect authenticated state
        await page.reload();
        await page.waitForURL('/', { timeout: 10000 });

        // Login modal should not be visible
        await expect(page.locator('[role="dialog"]')).not.toBeVisible({ timeout: 10000 });

        // App content should be visible
        await expect(page.locator('.project-item').first()).toBeVisible({ timeout: 10000 });
    });

    test('should not dismiss login modal when clicking outside', async ({ page }) => {
        await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 10000 });

        // Click on the overlay (outside the modal)
        await page.locator('.overlay').click();

        // Login modal should still be visible (overlay click is blocked)
        await expect(page.locator('[role="dialog"]')).toBeVisible();
        // App content should still be hidden
        await expect(page.locator('.sidebar')).not.toBeVisible();
    });

    test('should show error on wrong password', async ({ page }) => {
        await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 10000 });

        // Try wrong password via API
        const response = await page.context().request.post('/api/auth/login', {
            data: { password: 'wrong-password' },
        });
        expect(response.status()).toBe(401);

        // Login modal should still be visible (no valid session)
        await expect(page.locator('[role="dialog"]')).toBeVisible();
    });

    test('should logout and show login modal again', async ({ page }) => {
        // Login first
        await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 10000 });
        await page.context().request.post('/api/auth/login', {
            data: { password: 'test-password-1234' },
        });
        await page.reload();
        await expect(page.locator('[role="dialog"]')).not.toBeVisible({ timeout: 10000 });
        await expect(page.locator('.project-item').first()).toBeVisible({ timeout: 10000 });

        // Navigate to a project to see the top bar with settings button
        await page.click('.project-item');
        await page.waitForSelector('.card, .top-bar', { timeout: 10000 });

        // Click settings icon button in the top bar (not the sidebar one)
        const settingsBtn = page.locator('.icon-btn[aria-label="Settings"]');
        await expect(settingsBtn).toBeVisible({ timeout: 10000 });
        await settingsBtn.click();

        // Click "Sign out" button in settings modal footer
        const signOutBtn = page.locator('.btn-signout');
        await expect(signOutBtn).toBeVisible({ timeout: 10000 });
        await signOutBtn.click();

        // Confirm the sign out in the confirmation dialog
        await page.click('.btn-confirm.destructive');

        // Login modal should reappear
        await expect(page.locator('[role="dialog"] h2')).toHaveText('Sign in', { timeout: 10000 });
    });
});
