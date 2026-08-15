import { describe, it } from 'vitest';

/**
 * Chat rendering cost benchmarks.
 *
 * The virtual-scroll benchmarks that lived here tracked the chat
 * virtualizer, which was replaced by content-visibility-based rendering
 * (ChatEntries.svelte) — see git history. What remains is the markdown
 * rendering cost, which dominates per-message paint work.
 */

describe('chat performance benchmarks', () => {
    // Markdown rendering cost (via marked)
    it('markdown rendering cost (assistant content)', async () => {
        const { marked } = await import('marked');

        const samples = [
            { label: 'short', text: 'Here is the result. The file has been updated.' },
            { label: 'medium', text: 'I analyzed the code and found several issues.\n\n1. The first issue is in the main loop\n2. The second issue is in the error handling\n3. The third issue is in the config loading\n\nEach of these needs to be addressed separately. Here is some code:\n\n```typescript\nfunction example() {\n  return true;\n}\n```\nLet me know if you have questions.' },
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
