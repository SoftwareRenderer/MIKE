import { describe, it, expect } from 'vitest';
import { computeVirtualScrollRange } from '$lib/composables/use-virtual-scroll.svelte';

// ── Synthetic data generators ──

function randomInt(min: number, max: number): number {
    return Math.floor(Math.random() * (max - min + 1)) + min;
}

function generateUserEntry(): { role: string; content: string } {
    const lines = randomInt(1, 5);
    const content = Array.from({ length: lines }, () =>
        'x'.repeat(randomInt(20, 120))
    ).join('\n');
    return { role: 'user', content };
}

function generateAssistantEntry(): { role: string; content: string; toolCalls?: any[]; reasoningContent?: string } {
    const entry: any = { role: 'assistant', content: '' };

    // Some entries have reasoning
    if (Math.random() < 0.3) {
        entry.reasoningContent = 'x'.repeat(randomInt(100, 800));
    }

    // Text content
    const paragraphs = randomInt(1, 8);
    entry.content = Array.from({ length: paragraphs }, () =>
        'x'.repeat(randomInt(50, 300))
    ).join('\n\n');

    // Tool calls
    const numTools = Math.random() < 0.6 ? randomInt(1, 6) : 0;
    if (numTools > 0) {
        const toolNames = ['bash', 'read_file', 'write_file', 'edit_file'];
        entry.toolCalls = Array.from({ length: numTools }, () => {
            const toolName = toolNames[randomInt(0, toolNames.length - 1)];
            const tc: any = {
                toolId: `tc-${Math.random().toString(36).slice(2)}`,
                toolName,
                args: { path: '/src/file.ts' },
            };
            if (toolName === 'edit_file') {
                const editLines = randomInt(2, 30);
                tc.args = {
                    edits: Array.from({ length: randomInt(1, 3) }, () => ({
                        oldText: Array.from({ length: editLines }, () => 'old line x'.repeat(randomInt(1, 5))).join('\n'),
                        newText: Array.from({ length: editLines }, () => 'new line x'.repeat(randomInt(1, 5))).join('\n'),
                    })),
                };
            } else if (toolName === 'bash') {
                tc.result = Array.from({ length: randomInt(1, 15) }, () =>
                    'output line'.repeat(randomInt(1, 8))
                ).join('\n');
            }
            return tc;
        });
    }

    return entry;
}

/** Generate a realistic conversation with N entries (user + assistant pairs). */
function generateConversation(numEntries: number): any[] {
    const entries: any[] = [];
    for (let i = 0; i < numEntries; i++) {
        if (i % 2 === 0) {
            entries.push(generateUserEntry());
        } else {
            entries.push(generateAssistantEntry());
        }
    }
    return entries;
}

// ── Height estimation (matches ChatEntries.svelte logic) ──

function estimateTextHeight(text: string, base: number): number {
    if (!text) return base;
    const lines = text.split('\n').length;
    const chars = text.length;
    const wrappedLines = Math.ceil(chars / 70);
    const totalLines = Math.max(lines, wrappedLines);
    return base + totalLines * 20;
}

function estimateEditDiffHeight(tc: any): number {
    const edits = tc.args?.edits as Array<{ oldText: string; newText: string }> | undefined;
    if (!edits) return 30;

    let totalDiffLines = 0;
    let changes = 0;
    for (const edit of edits) {
        const oldLines = (edit.oldText || '').split('\n');
        const newLines = (edit.newText || '').split('\n');
        const pairCount = Math.min(oldLines.length, newLines.length);
        for (let p = 0; p < pairCount; p++) {
            if (oldLines[p] === newLines[p]) {
                totalDiffLines += 1;
            } else {
                totalDiffLines += 2;
                changes += 2;
            }
        }
        totalDiffLines += (oldLines.length - pairCount) + (newLines.length - pairCount);
        changes += (oldLines.length - pairCount) + (newLines.length - pairCount);
    }

    let h = 30;
    if (changes <= 2) {
        h += totalDiffLines * 20;
        h += edits.length * 5;
    }
    return h;
}

function getEstimatedHeight(entry: any, _index: number): number {
    if (entry.role === 'error') return estimateTextHeight(entry.content, 40);
    if (entry.role === 'summarization') return estimateTextHeight(entry.content, 60);
    if (entry.role === 'merge_resolution') return estimateTextHeight(entry.content, 50);
    if (entry.role === 'user') {
        let h = estimateTextHeight(entry.content, 35);
        if (entry.attachments?.length) h += entry.attachments.length * 80;
        return h;
    }
    // assistant
    let h = 30;
    h += estimateTextHeight(entry.content, 20);
    if (entry.reasoningContent) h += estimateTextHeight(entry.reasoningContent, 40);
    else if (entry.hasReasoning) h += 60;
    for (const tc of entry.toolCalls || []) {
        h += 60;
        if (tc.toolName === 'edit_file') {
            h += estimateEditDiffHeight(tc);
        } else if (tc.result) {
            h += estimateTextHeight(tc.result, 35);
        } else if (tc.isPending) {
            h += 30;
        }
    }
    if (entry.isStreaming) h += 20;
    if (entry.stats) h += 24;
    return h;
}

// ── Benchmarks ──

describe('chat performance benchmarks', () => {
    const sizes = [10, 25, 50, 100, 200, 500];

    for (const n of sizes) {
        it(`getEstimatedHeight for ${n} entries`, () => {
            const entries = generateConversation(n);
            const heights: number[] = [];

            const t0 = performance.now();
            for (let i = 0; i < entries.length; i++) {
                heights.push(getEstimatedHeight(entries[i], i));
            }
            const elapsed = performance.now() - t0;

            console.log(
                `  [height] n=${n.toString().padStart(4)}  ` +
                `${elapsed.toFixed(1)}ms  ` +
                `(${(elapsed / n).toFixed(2)}ms/entry)  ` +
                `totalHeight=${heights.reduce((a, b) => a + b, 0).toLocaleString()}px`
            );

            expect(elapsed).toBeLessThan(500); // sanity: should be fast
        });
    }

    for (const n of sizes) {
        it(`computeVirtualScrollRange for ${n} entries`, () => {
            const entries = generateConversation(n);
            const getHeight = (i: number) => getEstimatedHeight(entries[i], i);

            const t0 = performance.now();
            const result = computeVirtualScrollRange(n, getHeight, 0, 600, 5);
            const elapsed = performance.now() - t0;

            console.log(
                `  [vscroll] n=${n.toString().padStart(4)}  ` +
                `${elapsed.toFixed(1)}ms  ` +
                `visible=${result.startIdx}..${result.endIdx}  ` +
                `totalHeight=${result.totalHeight.toLocaleString()}px`
            );

            expect(elapsed).toBeLessThan(500);
        });
    }

    // Measure cumulative offset computation (the heavy part of virtual scroll)
    for (const n of sizes) {
        it(`offset computation (Float64Array) for ${n} entries`, () => {
            const entries = generateConversation(n);
            const getHeight = (i: number) => getEstimatedHeight(entries[i], i);

            const t0 = performance.now();
            const offsets = new Float64Array(n + 1);
            let sum = 0;
            for (let i = 0; i < n; i++) {
                sum += getHeight(i);
                offsets[i + 1] = sum;
            }
            const elapsed = performance.now() - t0;

            console.log(
                `  [offsets] n=${n.toString().padStart(4)}  ` +
                `${elapsed.toFixed(1)}ms  ` +
                `total=${sum.toLocaleString()}px`
            );

            expect(elapsed).toBeLessThan(500);
        });
    }

    // Binary search cost (what happens on each scroll event)
    it('binary search on scroll (various sizes)', () => {
        for (const n of sizes) {
            const entries = generateConversation(n);
            const getHeight = (i: number) => getEstimatedHeight(entries[i], i);

            // Build offsets once
            const offsets = new Float64Array(n + 1);
            let sum = 0;
            for (let i = 0; i < n; i++) {
                sum += getHeight(i);
                offsets[i + 1] = sum;
            }

            // Simulate 100 scroll events at random positions
            const scrollPositions = Array.from({ length: 100 }, () => randomInt(0, Math.max(1, sum - 600)));

            const t0 = performance.now();
            for (const scrollTop of scrollPositions) {
                const viewTop = scrollTop;
                const viewBottom = scrollTop + 600;

                // Binary search for first visible
                let lo = 0, hi = n;
                while (lo < hi) {
                    const mid = (lo + hi) >> 1;
                    if (offsets[mid] + getHeight(mid) <= viewTop) {
                        lo = mid + 1;
                    } else {
                        hi = mid;
                    }
                }

                // Binary search for last visible
                lo = lo;
                hi = n;
                while (lo < hi) {
                    const mid = (lo + hi) >> 1;
                    if (offsets[mid] < viewBottom) {
                        lo = mid + 1;
                    } else {
                        hi = mid;
                    }
                }
            }
            const elapsed = performance.now() - t0;

            console.log(
                `  [bsearch] n=${n.toString().padStart(4)}  ` +
                `100 scrolls=${elapsed.toFixed(1)}ms  ` +
                `(${(elapsed / 100).toFixed(3)}ms/scroll)`
            );
        }
    });

    // Markdown rendering cost (via marked)
    it('markdown rendering cost (assistant content)', async () => {
        const { marked } = await import('marked');

        const samples = [
            { label: 'short', text: 'Here is the result. The file has been updated.' },
            { label: 'medium', text: 'I analyzed the code and found several issues.\n\n1. The first issue is in the main loop\n2. The second issue is in the error handling\n3. The third issue is in the config loading\n\nEach of these needs to be addressed separately. Here is some code:\n\n```typescript\nfunction example() {\n  return true;\n}\n```\n\nLet me know if you have questions.' },
            { label: 'long', text: Array.from({ length: 20 }, (_, i) => {
                const code = i % 3 === 0 ? '\n```typescript\nconst x = ' + i + ';\n```\n' : '';
                return `Paragraph ${i + 1}: This is a detailed explanation of the changes made to the codebase. The function handles edge cases properly and includes proper error handling.${code}`;
            }).join('\n\n') },
        ];

        for (const { label, text } of samples) {
            const t0 = performance.now();
            marked.parse(text, { async: false });
            const elapsed = performance.now() - t0;
            console.log(
                `  [markdown] ${label.padEnd(8)}  ` +
                `${text.length.toString().padStart(6)} chars  ` +
                `${elapsed.toFixed(1)}ms`
            );
        }
    });
});
