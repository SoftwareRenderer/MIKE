// Throwaway: pixel-diff two screenshots and report how many pixels differ and where.
import { chromium } from 'playwright';
import { readFileSync } from 'fs';

const pairs = process.argv.slice(2);
const browser = await chromium.launch();
const page = await browser.newPage();

async function data(p) {
    return 'data:image/png;base64,' + readFileSync(p).toString('base64');
}

for (const pair of pairs) {
    const [a, b] = pair.split(',');
    const out = await page.evaluate(async ([da, db]) => {
        const load = (src) => new Promise((res) => { const i = new Image(); i.onload = () => res(i); i.src = src; });
        const [ia, ib] = await Promise.all([load(da), load(db)]);
        const c = document.createElement('canvas');
        c.width = Math.max(ia.width, ib.width); c.height = Math.max(ia.height, ib.height);
        const g = c.getContext('2d', { willReadFrequently: true });
        g.drawImage(ia, 0, 0);
        const A = g.getImageData(0, 0, c.width, c.height).data;
        g.clearRect(0, 0, c.width, c.height);
        g.drawImage(ib, 0, 0);
        const B = g.getImageData(0, 0, c.width, c.height).data;
        let n = 0, minX = 1e9, minY = 1e9, maxX = -1, maxY = -1, maxDelta = 0;
        for (let i = 0; i < A.length; i += 4) {
            const d = Math.abs(A[i] - B[i]) + Math.abs(A[i + 1] - B[i + 1]) + Math.abs(A[i + 2] - B[i + 2]);
            if (d > 0) {
                n++;
                maxDelta = Math.max(maxDelta, d);
                const px = (i / 4) % c.width, py = Math.floor((i / 4) / c.width);
                minX = Math.min(minX, px); minY = Math.min(minY, py);
                maxX = Math.max(maxX, px); maxY = Math.max(maxY, py);
            }
        }
        return { size: [c.width, c.height], differing: n, maxDelta, box: n ? [minX, minY, maxX, maxY] : null };
    }, [await data(a), await data(b)]);
    console.log(`${a.split('/').pop().replace('vf-BASE-', '').replace('vf-MINE-', '')} vs partner: ${out.differing} of ${out.size[0] * out.size[1]} px differ (max channel-sum ${out.maxDelta}) box=${JSON.stringify(out.box)}`);
}
await browser.close();
