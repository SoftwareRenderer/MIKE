/**
 * Controlled virtualization benchmark.
 *
 * Run: node frontend/tests/bench-chat-virtual.mjs
 * No server required.
 */
import { chromium } from 'playwright';
import { readFile } from 'fs/promises';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';

const __dirname = dirname(fileURLToPath(import.meta.url));

(async () => {
    const browser = await chromium.launch();
    const page = await browser.newPage();

    const html = await readFile(join(__dirname, 'bench-chat-virtual.html'), 'utf-8');
    await page.setContent(html);

    const sizes = [10, 25, 50, 100, 200, 500];

    console.log('\n=== Virtualization Benchmark (headless Chromium) ===\n');
    console.log('Measures: height estimation, offset computation, binary search, DOM rendering\n');

    for (const n of sizes) {
        const result = await page.evaluate((count) => runBenchmark(count), n);

        console.log(`${n.toString().padStart(4)} entries (${Math.ceil(n / 2)} user+assistant pairs):`);
        console.log(`  Height estimation:  ${result.heightTime.padStart(6)}ms  (${(parseFloat(result.heightTime) / n).toFixed(3)}ms/entry)`);
        console.log(`  Offset computation: ${result.offsetTime.padStart(6)}ms`);
        console.log(`  Binary search:      ${result.bsearchTime.padStart(6)}ms for 100 scrolls  (${result.bsearchPerScroll}\u00b5s/scroll)`);
        console.log(`  DOM rendering:      ${result.domTime.padStart(6)}ms  (${(parseFloat(result.domTime) / n).toFixed(2)}ms/entry)`);
        console.log(`  DOM nodes:          ${result.nodeCount.toString().padStart(6)}  (${(result.nodeCount / n).toFixed(1)} per entry)`);
        console.log(`  Total height:       ${parseInt(result.totalHeight).toLocaleString().padStart(10)}px  (avg ${result.avgHeight}px/entry)`);
        console.log();
    }

    await browser.close();
    console.log('=== Done ===\n');
})();
