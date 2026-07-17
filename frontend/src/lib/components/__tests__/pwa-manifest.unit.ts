import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));

describe('PWA Manifest', () => {
    it('should have a valid web app manifest configuration', () => {
        // Read the vite config to extract the manifest configuration
        const viteConfigPath = resolve(__dirname, '../../../../vite.config.ts');
        const configContent = readFileSync(viteConfigPath, 'utf-8');

        // Extract manifest configuration from the vite config
        // The manifest is defined inline in VitePWA plugin configuration
        expect(configContent).toContain('name:');
        expect(configContent).toContain('short_name:');
        expect(configContent).toContain('start_url:');
        expect(configContent).toContain('display:');
        expect(configContent).toContain('icons:');

        // Verify theme colors
        expect(configContent).toContain("theme_color: '#141414'");
        expect(configContent).toContain("background_color: '#141414'");

        // Verify icon sizes are configured
        expect(configContent).toContain("sizes: '192x192'");
        expect(configContent).toContain("sizes: '512x512'");

        // Verify PWA icons exist in public directory
        const icon192Path = resolve(__dirname, '../../../../public/pwa-icon-192.png');
        const icon512Path = resolve(__dirname, '../../../../public/pwa-icon-512.png');
        expect(readFileSync(icon192Path).length).toBeGreaterThan(0);
        expect(readFileSync(icon512Path).length).toBeGreaterThan(0);
    });
});
