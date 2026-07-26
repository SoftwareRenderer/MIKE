import { test, expect } from '@playwright/test';
import { apiPutConfig, apiGetConfig, waitForConfig, csrfFetch } from './helpers';

test.describe.serial('Agent provider without extension', () => {
    test.beforeEach(async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.project-item', { timeout: 10000 });
    });

    test('should not loop on extensions endpoint when agent provider exists but no agent extensions', async ({ page }) => {
        // 1. Set up config with an agent provider using agent:// URL scheme
        // (backend auto-detects agent providers by URL prefix)
        const currentConfig = await apiGetConfig(page);
        await apiPutConfig(page, {
            providers: [
                ...(currentConfig?.providers || []),
                {
                    name: 'pi-agent',
                    display_name: 'Pi Coding Agent',
                    kind: 'agent',
                    url: 'agent://pi',
                    models: [],
                    concurrency: 1,
                    default_model: '',
                },
            ],
        });

        // Verify the agent provider was saved
        const config = await apiGetConfig(page);
        const agentProvider = config.providers.find((p: any) => p.url?.startsWith('agent://'));
        expect(agentProvider).toBeDefined();

        // 2. Navigate fresh and track API calls
        await page.goto('/');
        await page.waitForSelector('.project-item', { timeout: 10000 });

        // Track API calls to /api/extensions
        let extensionsCallCount = 0;
        await page.route('**/api/extensions', async (route) => {
            extensionsCallCount++;
            await route.continue();
        });

        // 3. Open settings (triggers loadAvailableAgents once)
        await page.click('.icon-btn[aria-label="Settings"]');
        await page.waitForSelector('[role="dialog"]', { timeout: 5000 });

        // Wait for initial API calls to complete
        await page.waitForTimeout(500);
        const callsAfterOpen = extensionsCallCount;

        // 4. Wait and verify no additional calls are made (no loop)
        // With the fix (agentsLoaded flag), re-running $effect should NOT trigger more fetches
        await page.waitForTimeout(1500);
        const callsAfterWait = extensionsCallCount;

        // Should have made at least 1 call on open, but no additional calls during wait
        expect(callsAfterOpen).toBeGreaterThanOrEqual(1);
        expect(callsAfterWait).toBe(callsAfterOpen); // no loop!

        // 5. Verify the agent provider card is visible
        const agentCard = page.locator('.provider-card').filter({ hasText: 'Pi Coding Agent' });
        await expect(agentCard).toBeVisible();

        // 6. Remove the agent provider via UI
        await agentCard.locator('.btn-remove-provider').click();

        // Handle confirmation dialog
        await page.waitForTimeout(200);
        const removeBtn = page.locator('button:has-text("Remove")').last();
        if (await removeBtn.isVisible().catch(() => false)) {
            await removeBtn.click();
        }
        await page.waitForTimeout(300);

        // 7. Verify the provider was removed from the list
        await expect(agentCard).not.toBeVisible();

        // 8. Save and verify via API
        await page.getByRole('button', { name: 'Save' }).click();
        await expect(page.locator('.modal')).not.toBeVisible({ timeout: 10000 });

        const finalConfig = await apiGetConfig(page);
        const remainingAgent = finalConfig.providers.find((p: any) => p.url?.startsWith('agent://'));
        expect(remainingAgent).toBeUndefined();

        // 9. Final assertion: total calls should be reasonable (not dozens from a loop)
        expect(extensionsCallCount).toBeLessThan(10);
    });

    test('should render agent provider card without crashing when no agent extensions installed', async ({ page }) => {
        // Set up config with an agent provider but no agent extensions
        const currentConfig = await apiGetConfig(page);
        await apiPutConfig(page, {
            providers: [
                ...(currentConfig?.providers || []),
                {
                    name: 'orphan-agent',
                    display_name: 'Orphan Agent',
                    kind: 'agent',
                    url: 'agent://nonexistent-agent',
                    models: [],
                    concurrency: 1,
                    default_model: '',
                },
            ],
        });

        // Verify the agent provider was saved
        const config = await apiGetConfig(page);
        const agentProvider = config.providers.find((p: any) => p.name === 'orphan-agent');
        expect(agentProvider).toBeDefined();

        // Navigate fresh and open settings
        await page.goto('/');
        await page.waitForSelector('.project-item', { timeout: 10000 });

        await page.click('.icon-btn[aria-label="Settings"]');
        await page.waitForSelector('[role="dialog"]', { timeout: 5000 });

        // Should show the agent provider card without errors
        const agentCard = page.locator('.provider-card').filter({ hasText: 'Orphan Agent' });
        await expect(agentCard).toBeVisible({ timeout: 5000 });

        // The agent icon should be visible
        await expect(agentCard.locator('.agent-icon')).toBeVisible();

        // Clean up: remove the provider
        await agentCard.locator('.btn-remove-provider').click();
        await page.waitForTimeout(200);
        const removeBtn = page.locator('button:has-text("Remove")').last();
        if (await removeBtn.isVisible().catch(() => false)) {
            await removeBtn.click();
        }
        await page.waitForTimeout(300);

        await page.getByRole('button', { name: 'Save' }).click();
        await expect(page.locator('.modal')).not.toBeVisible({ timeout: 10000 });
    });
});
