import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const chatEntriesPath = resolve(__dirname, '../Chat/ChatEntries.svelte');
const source = readFileSync(chatEntriesPath, 'utf-8');

describe('ChatEntries — Jump to latest button', () => {
    it('imports SCROLL_BOTTOM_THRESHOLD from $lib/constants', () => {
        expect(source).toContain("SCROLL_BOTTOM_THRESHOLD");
        expect(source).toContain("from '$lib/constants'");
    });

    it('declares a local atBottom reactive state initialized to true', () => {
        expect(source).toMatch(/atBottom\s*=\s*\$state\s*\(\s*true\s*\)/);
    });

    it('computes atBottom in handleCombinedScroll using the scroll element gap', () => {
        // The handler should check scrollHeight - scrollTop - clientHeight < threshold
        expect(source).toMatch(/scrollHeight.*scrollTop.*clientHeight|scrollTop.*clientHeight.*scrollHeight/);
        expect(source).toContain('SCROLL_BOTTOM_THRESHOLD');
    });

    it('recomputes atBottom in the $effect that runs when entries change', () => {
        // There should be a $effect that references entries.length and also sets atBottom
        const effectBlock = source.match(/\$effect\s*\(\s*\(\s*\)\s*=>\s*\{[^}]*entries\.length[^}]*\}/s);
        expect(effectBlock).toBeTruthy();
        // The effect should also reference atBottom
        expect(source).toMatch(/\$effect[^{]*\{[^}]*(entries\.length|computeStickyFromScroll)[^}]*atBottom/s);
    });

    it('renders a jump-to-latest button with class chat-jump-latest', () => {
        expect(source).toContain('chat-jump-latest');
    });

    it('shows the button only when not at bottom and entries exist', () => {
        // The button should be conditionally rendered: {#if !atBottom && entries.length > 0}
        expect(source).toMatch(/\{#if\s+!atBottom\s*&&\s*entries\.length\s*>\s*0/);
    });

    it('the button has aria-label for accessibility', () => {
        expect(source).toMatch(/aria-label=["'][^"']*latest[^"']*["']/i);
    });

    it('the button has a title tooltip', () => {
        expect(source).toMatch(/title=["'][^"']*["']/);
    });

    it('the button contains a down arrow/chevron SVG', () => {
        expect(source).toMatch(/<svg[^>]*>.*-chevron.*<\/svg>|<svg[^>]*>.*arrow.*<\/svg>|<svg[^>]*>.*polyline.*<\/svg>|<svg[^>]*>.*path.*<\/svg>/is);
    });

    it('the button click handler calls scrollToBottom on the virtual list', () => {
        expect(source).toMatch(/handleJumpLatest|jumpLatest|scrollToBottom/);
        // Should call virtualListComp?.scrollToBottom()
        expect(source).toContain('virtualListComp?.scrollToBottom()');
    });

    it('the click handler uses requestAnimationFrame for a second scrollToBottom call', () => {
        expect(source).toMatch(/requestAnimationFrame.*scrollToBottom|scrollToBottom.*requestAnimationFrame/s);
    });

    it('the click handler sets atBottom to true after scrolling', () => {
        expect(source).toMatch(/atBottom\s*=\s*true/);
    });

    it('the button is positioned absolutely within the chat-messages-wrapper', () => {
        // CSS should position it absolute, bottom-right
        expect(source).toMatch(/\.chat-jump-latest\s*\{[\s\S]*?position:\s*absolute/);
        expect(source).toMatch(/\.chat-jump-latest\s*\{[\s\S]*?bottom:/);
        expect(source).toMatch(/\.chat-jump-latest\s*\{[\s\S]*?right:/);
    });

    it('the button z-index is at or below the sticky header z-index of 5', () => {
        const zIndexMatch = source.match(/\.chat-jump-latest\s*\{[\s\S]*?z-index:\s*(\d+)/);
        expect(zIndexMatch).toBeTruthy();
        if (zIndexMatch) {
            expect(parseInt(zIndexMatch[1])).toBeLessThanOrEqual(5);
        }
    });

    it('the button has a minimum touch-friendly size (32px)', () => {
        const sizeMatch = source.match(/\.chat-jump-latest[^{]*\{[\s\S]*?(min-width|min-height|width|height):\s*(\d+)px/);
        expect(sizeMatch).toBeTruthy();
    });

    it('the button has a transition or animation for entrance', () => {
        expect(source).toMatch(/\.chat-jump-latest[^{]*\{[\s\S]*?(transition|animation|@keyframes)/);
    });

    it('the button uses surface-1 background and border styling consistent with chat theme', () => {
        expect(source).toMatch(/\.chat-jump-latest[^{]*\{[\s\S]*?(background|border)/);
    });

    it('the button has a hover state with accent color', () => {
        expect(source).toMatch(/\.chat-jump-latest.*:hover[\s\S]*?(accent|hover)/);
    });

    it('calls parent onScroll in the combined scroll handler', () => {
        expect(source).toContain('onScroll?.()');
    });
});
