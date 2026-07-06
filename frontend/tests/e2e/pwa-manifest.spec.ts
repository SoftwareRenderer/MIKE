import { test, expect } from '@playwright/test';

test.describe('PWA Manifest', () => {
    test.beforeEach(async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });
    });

    test('should serve a valid web app manifest', async ({ request }) => {
        const response = await request.get('/manifest.webmanifest');
        expect(response.status()).toBe(200);

        const manifest = await response.json();

        // Required fields for PWA installability
        expect(manifest.name).toBeTruthy();
        expect(manifest.short_name).toBeTruthy();
        expect(manifest.start_url).toBeTruthy();
        expect(manifest.display).toBeTruthy();
        expect(manifest.icons).toBeInstanceOf(Array);
        expect(manifest.icons.length).toBeGreaterThan(0);

        // Theme color
        expect(manifest.theme_color).toBe('#141414');
        expect(manifest.background_color).toBe('#141414');

        // Icons should include 192x192 and 512x512
        const sizes = manifest.icons.map((icon: { sizes: string }) => icon.sizes);
        expect(sizes).toContain('192x192');
        expect(sizes).toContain('512x512');
    });

    test('should serve the service worker', async ({ request }) => {
        const response = await request.get('/sw.js');
        expect(response.status()).toBe(200);
        const body = await response.text();
        expect(body).toContain('precacheAndRoute');
    });

    test('should serve PWA icons', async ({ request }) => {
        const icon192 = await request.get('/pwa-icon-192.png');
        expect(icon192.status()).toBe(200);
        expect(icon192.headers()['content-type']).toBe('image/png');

        const icon512 = await request.get('/pwa-icon-512.png');
        expect(icon512.status()).toBe(200);
        expect(icon512.headers()['content-type']).toBe('image/png');
    });

    test('manifest should be accessible without authentication', async ({ request }) => {
        // Create a new browser context without any cookies
        const response = await request.get('/manifest.webmanifest');
        expect(response.status()).toBe(200);
    });
});
