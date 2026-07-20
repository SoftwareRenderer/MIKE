/**
 * Realistic chat rendering benchmark.
 *
 * Measures the cost of operations that actually happen during chat rendering:
 * 1. Height estimation (getEstimatedHeight)
 * 2. Markdown parsing (marked.parse) — per assistant message
 * 3. Offset computation (Float64Array)
 * 4. Binary search for scroll
 * 5. DOM node count (with realistic structure including tool calls)
 * 6. Svelte-style reactive overhead simulation
 *
 * Run: node frontend/tests/bench-chat-realistic.mjs
 */
import { chromium } from 'playwright';
import { readFile } from 'fs/promises';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';

const __dirname = dirname(fileURLToPath(import.meta.url));

const BENCH_HTML = `<!DOCTYPE html>
<html>
<head><title>Benchmark</title>
<script src="https://cdn.jsdelivr.net/npm/marked/marked.min.js"><\/script>
</head>
<body>
<script>
function createUserEntry(id) {
    const lines = Math.floor(Math.random() * 4) + 1;
    return {
        id: 'user-' + id, role: 'user',
        content: Array.from({ length: lines }, () =>
            'Lorem ipsum dolor sit amet consectetur adipiscing elit '.repeat(Math.floor(Math.random() * 3) + 1)
        ).join('\\n'),
    };
}

function createAssistantEntry(id) {
    const paragraphs = Math.floor(Math.random() * 6) + 1;
    const content = Array.from({ length: paragraphs }, () =>
        'This is a paragraph explaining the changes made. The code has been updated to handle edge cases properly and includes proper error handling for all input validation.'.repeat(Math.floor(Math.random() * 3) + 1)
    ).join('\\n\\n');

    const entry = { id: 'assistant-' + id, role: 'assistant', content, toolCalls: [] };

    const numTools = Math.floor(Math.random() * 5) + 1;
    const toolNames = ['bash', 'read_file', 'write_file', 'edit_file'];
    for (let t = 0; t < numTools; t++) {
        const toolName = toolNames[Math.floor(Math.random() * toolNames.length)];
        const tc = { toolId: 'tc-' + id + '-' + t, toolName, args: { path: '/src/file.ts' }, success: true };
        if (toolName === 'edit_file') {
            const n = Math.floor(Math.random() * 20) + 2;
            tc.args = { edits: [{
                oldText: Array.from({ length: n }, () => 'const x = someVariableThatDoesSomething;').join('\\n'),
                newText: Array.from({ length: n }, () => 'const y = refactoredVariableThatDoesSomethingBetter();').join('\\n'),
            }]};
        } else if (toolName === 'bash') {
            tc.result = Array.from({ length: Math.floor(Math.random() * 15) + 1 }, () =>
                'output line from command execution with some details'.repeat(Math.floor(Math.random() * 4) + 1)
            ).join('\\n');
        }
        entry.toolCalls.push(tc);
    }
    return entry;
}

function estimateTextHeight(text, base) {
    if (!text) return base;
    const lines = text.split('\\n').length;
    const chars = text.length;
    const wrappedLines = Math.ceil(chars / 70);
    return base + Math.max(lines, wrappedLines) * 20;
}

function estimateEditDiffHeight(tc) {
    const edits = tc.args?.edits;
    if (!edits) return 30;
    let totalDiffLines = 0, changes = 0;
    for (const edit of edits) {
        const oldLines = (edit.oldText || '').split('\\n');
        const newLines = (edit.newText || '').split('\\n');
        const pairCount = Math.min(oldLines.length, newLines.length);
        for (let p = 0; p < pairCount; p++) {
            if (oldLines[p] === newLines[p]) totalDiffLines += 1;
            else { totalDiffLines += 2; changes += 2; }
        }
        totalDiffLines += (oldLines.length - pairCount) + (newLines.length - pairCount);
        changes += (oldLines.length - pairCount) + (newLines.length - pairCount);
    }
    let h = 30;
    if (changes <= 2) { h += totalDiffLines * 20; h += edits.length * 5; }
    return h;
}

function getEstimatedHeight(entry) {
    if (entry.role === 'user') return estimateTextHeight(entry.content, 35);
    let h = 30 + estimateTextHeight(entry.content, 20);
    for (const tc of entry.toolCalls || []) {
        h += 60;
        if (tc.toolName === 'edit_file') h += estimateEditDiffHeight(tc);
        else if (tc.result) h += estimateTextHeight(tc.result, 35);
    }
    return h;
}

async function runBenchmark(count) {
    const entries = [];
    for (let i = 0; i < count; i++) {
        entries.push(i % 2 === 0 ? createUserEntry(i) : createAssistantEntry(i));
    }

    // 1: Height estimation
    const t0 = performance.now();
    const heights = entries.map(e => getEstimatedHeight(e));
    const heightTime = performance.now() - t0;

    // 2: Markdown parsing (only for assistant messages) — this is the real cost
    const t1 = performance.now();
    let totalMarkdownChars = 0;
    for (const entry of entries) {
        if (entry.role === 'assistant' && entry.content) {
            totalMarkdownChars += entry.content.length;
            marked.parse(entry.content, { async: false });
        }
        // Also parse tool results that use markdown
        if (entry.toolCalls) {
            for (const tc of entry.toolCalls) {
                if (tc.toolName === 'request_review' && tc.args?.summary) {
                    totalMarkdownChars += tc.args.summary.length;
                    marked.parse(tc.args.summary, { async: false });
                }
            }
        }
    }
    const markdownTime = performance.now() - t1;

    // 3: Offset computation
    const t2 = performance.now();
    const offsets = new Float64Array(count + 1);
    let totalHeight = 0;
    for (let i = 0; i < count; i++) {
        totalHeight += heights[i];
        offsets[i + 1] = totalHeight;
    }
    const offsetTime = performance.now() - t2;

    // 4: Binary search (100 scrolls)
    const t3 = performance.now();
    for (let s = 0; s < 100; s++) {
        const scrollTop = Math.floor(Math.random() * totalHeight);
        const viewBottom = scrollTop + 600;
        let lo = 0, hi = count;
        while (lo < hi) {
            const mid = (lo + hi) >> 1;
            if (offsets[mid] + heights[mid] <= scrollTop) lo = mid + 1; else hi = mid;
        }
        lo = lo; hi = count;
        while (lo < hi) {
            const mid = (lo + hi) >> 1;
            if (offsets[mid] < viewBottom) lo = mid + 1; else hi = mid;
        }
    }
    const bsearchTime = performance.now() - t3;

    // 5: Full DOM rendering (all entries, no virtualization)
    const t4 = performance.now();
    const container = document.createElement('div');
    container.style.cssText = 'position:absolute;left:-9999px;top:-9999px;width:600px;height:600px;overflow:auto;';

    for (const entry of entries) {
        const div = document.createElement('div');
        div.className = 'chat-message';
        const wrapper = document.createElement('div');
        wrapper.className = 'message-wrapper';

        if (entry.role === 'assistant') {
            // Render markdown as HTML (like Svelte does with {@html})
            const bubble = document.createElement('div');
            bubble.className = 'message-bubble';
            const text = document.createElement('div');
            text.className = 'message-text';
            text.innerHTML = marked.parse(entry.content, { async: false });
            bubble.appendChild(text);
            wrapper.appendChild(bubble);
        } else {
            const bubble = document.createElement('div');
            bubble.className = 'message-bubble';
            const text = document.createElement('div');
            text.className = 'message-text';
            text.textContent = entry.content;
            bubble.appendChild(text);
            wrapper.appendChild(bubble);
        }

        // Tool calls
        if (entry.toolCalls) {
            for (const tc of entry.toolCalls) {
                const tcDiv = document.createElement('div');
                tcDiv.className = 'tool-call-entry';
                const card = document.createElement('div');
                card.className = 'tool-call-card';
                const header = document.createElement('div');
                header.className = 'tool-call-header';
                const icon = document.createElement('span');
                icon.className = 'tool-icon';
                header.appendChild(icon);
                const name = document.createElement('span');
                name.className = 'tool-name';
                name.textContent = tc.toolName;
                header.appendChild(name);
                const status = document.createElement('span');
                status.className = 'tool-status success';
                status.textContent = 'Completed';
                header.appendChild(status);
                card.appendChild(header);

                if (tc.result) {
                    const result = document.createElement('pre');
                    result.className = 'tool-result';
                    result.textContent = tc.result;
                    card.appendChild(result);
                }
                if (tc.duration !== undefined) {
                    const dur = document.createElement('span');
                    dur.className = 'tool-duration';
                    dur.textContent = tc.duration + 'ms';
                    card.appendChild(dur);
                }

                tcDiv.appendChild(card);
                wrapper.appendChild(tcDiv);
            }
        }

        // Message actions (always present)
        const actions = document.createElement('div');
        actions.className = 'message-actions';
        const copyBtn = document.createElement('button');
        copyBtn.className = 'action-btn';
        copyBtn.textContent = 'Copy';
        actions.appendChild(copyBtn);
        const editBtn = document.createElement('button');
        editBtn.className = 'action-btn';
        editBtn.textContent = 'Edit';
        actions.appendChild(editBtn);
        wrapper.appendChild(actions);

        div.appendChild(wrapper);
        container.appendChild(div);
    }
    document.body.appendChild(container);
    const scrollHeight = container.scrollHeight;
    const domTime = performance.now() - t4;
    const allNodes = container.querySelectorAll('*');
    const nodeCount = allNodes.length;
    document.body.removeChild(container);

    // Count stats
    const assistantCount = entries.filter(e => e.role === 'assistant').length;
    const totalToolCalls = entries.reduce((sum, e) => sum + (e.toolCalls?.length || 0), 0);

    return {
        heightTime: heightTime.toFixed(2),
        markdownTime: markdownTime.toFixed(2),
        markdownChars: totalMarkdownChars,
        offsetTime: offsetTime.toFixed(2),
        bsearchTime: bsearchTime.toFixed(2),
        bsearchPerScroll: (bsearchTime / 100 * 1000).toFixed(3),
        domTime: domTime.toFixed(2),
        nodeCount,
        totalHeight,
        avgHeight: (totalHeight / count).toFixed(0),
        assistantCount,
        totalToolCalls,
    };
}
<\/script>
</body>
</html>`;

(async () => {
    const browser = await chromium.launch();
    const page = await browser.newPage();

    // Load marked.js first, then our benchmark
    await page.setContent(BENCH_HTML, { waitUntil: 'networkidle' });

    // Wait for marked to be loaded
    await page.waitForFunction(() => typeof marked !== 'undefined', { timeout: 10000 });

    const sizes = [10, 25, 50, 100, 200, 500];

    console.log('\n=== Realistic Chat Rendering Benchmark (headless Chromium) ===\n');
    console.log('Includes: height estimation, markdown parsing (marked), offset computation, binary search, DOM rendering\n');

    for (const n of sizes) {
        const result = await page.evaluate((count) => runBenchmark(count), n);

        console.log(`${n.toString().padStart(4)} entries (${result.assistantCount} assistant msgs, ${result.totalToolCalls} tool calls):`);
        console.log(`  Height estimation:  ${result.heightTime.padStart(6)}ms  (${(parseFloat(result.heightTime) / n).toFixed(3)}ms/entry)`);
        console.log(`  Markdown parsing:   ${result.markdownTime.padStart(6)}ms  (${result.markdownChars.toLocaleString()} chars, ${(parseFloat(result.markdownTime) / result.assistantCount).toFixed(2)}ms/msg)`);
        console.log(`  Offset computation: ${result.offsetTime.padStart(6)}ms`);
        console.log(`  Binary search:      ${result.bsearchTime.padStart(6)}ms for 100 scrolls  (${result.bsearchPerScroll}\u00b5s/scroll)`);
        console.log(`  DOM rendering:      ${result.domTime.padStart(6)}ms  (${(parseFloat(result.domTime) / n).toFixed(2)}ms/entry)`);
        console.log(`  DOM nodes:          ${result.nodeCount.toString().padStart(6)}  (${(result.nodeCount / n).toFixed(1)} per entry)`);
        console.log(`  Total height:       ${parseInt(result.totalHeight).toLocaleString().padStart(10)}px  (avg ${result.avgHeight}px/entry)`);

        // Virtualization savings estimate
        const visibleEntries = Math.ceil(600 / (parseInt(result.totalHeight) / n)) + 10; // viewport + overscan
        const virtualizedEntries = Math.min(visibleEntries, n);
        const virtualizedDomTime = (parseFloat(result.domTime) / n * virtualizedEntries).toFixed(2);
        const savings = ((1 - virtualizedEntries / n) * 100).toFixed(0);
        console.log(`  ── Virtualization saves ~${savings}% DOM rendering (${result.domTime}ms → ~${virtualizedDomTime}ms, renders ${virtualizedEntries}/${n} entries) ──`);
        console.log();
    }

    await browser.close();
    console.log('=== Done ===\n');
})();
