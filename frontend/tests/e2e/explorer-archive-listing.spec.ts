import { test, expect, type Page } from '@playwright/test';
import { existsSync, rmSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { gzipSync } from 'node:zlib';
import { getProjectId, gotoProject, isolatedProject, ownProjectPath, selectProject } from './helpers';

// ============================================================
// Opening an archive in the Explorer lists its members
// ============================================================
//
// A Go walk over the archive's bytes and a browser's table only meet here: a real file in a real project,
// read through the jailed file path, drawn in the tab the Explorer opened. Both halves are unit-tested.
//
// Each test writes into its own project directory (isolatedProject): the file it lists is a real file in a
// tree other specs' git status reads. It starts no turn and reads no config.

isolatedProject();

/** One member of the fixture archive: `body` empty means a directory entry. */
type Member = { name: string; body?: string; dir?: boolean };

/** The stamp every member carries: 2023-11-14T22:13:20Z. */
const STAMP = 1700000000;

/**
 * A tar archive built here rather than shelled out for, so the bytes are the same on every machine and no
 * temporary directory is left behind. Every numeric field is fixed-width octal, and the checksum covers the
 * header with its own field standing in as spaces.
 */
function tar(members: Member[]): Buffer {
  const blocks: Buffer[] = [];
  for (const m of members) {
    const body = m.dir ? Buffer.alloc(0) : Buffer.from(m.body ?? '', 'utf8');
    const header = Buffer.alloc(512);
    /** One fixed-width octal field, as the header's own width demands. */
    const octal = (value: number, at: number, width: number) =>
      header.write(value.toString(8).padStart(width - 1, '0') + '\0', at);
    const text = (value: string, at: number) => header.write(value, at, 'utf8');

    text(m.dir ? `${m.name}/` : m.name, 0);
    octal(m.dir ? 0o755 : 0o644, 100, 8);   // mode
    octal(0, 108, 8);                       // uid
    octal(0, 116, 8);                       // gid
    octal(body.length, 124, 12);            // size
    octal(STAMP, 136, 12);                  // modified
    text('        ', 148);                  // checksum: eight spaces while it is being summed
    text(m.dir ? '5' : '0', 156);           // typeflag
    text('ustar\0', 257);
    text('00', 262);
    let sum = 0;
    for (const byte of header) sum += byte;
    text(sum.toString(8).padStart(6, '0') + '\0 ', 148);

    blocks.push(header);
    if (body.length > 0) {
      blocks.push(body);
      const pad = body.length % 512;
      if (pad) blocks.push(Buffer.alloc(512 - pad));
    }
  }
  blocks.push(Buffer.alloc(1024)); // two zero blocks end the archive
  return Buffer.concat(blocks);
}

function projectDir(): string {
  const dir = ownProjectPath();
  if (!dir) throw new Error('this spec must call isolatedProject() to own a project directory');
  return dir;
}

/** Write one file into the project the worker owns and open it from the Explorer's listing. */
async function writeAndOpen(page: Page, name: string, bytes: Buffer): Promise<string> {
  const target = join(projectDir(), name);
  writeFileSync(target, bytes);
  const projectId = await getProjectId(page);
  await selectProject(page, projectId);
  await expect(page.locator('.top-bar')).toBeVisible({ timeout: 10000 });
  await page.locator('.explorer-btn').click();
  await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 10000 });
  await expect(page.locator('.file-browser-item', { hasText: name })).toBeVisible({ timeout: 10000 });
  await page.locator('.file-browser-item', { hasText: name }).click();
  return target;
}

const MEMBERS: Member[] = [
  { name: 'notes', dir: true },
  { name: 'notes/todo.md', body: '# the list\n- open the archive\n' },
  { name: 'report.txt', body: 'one thousand and one lines\n' },
];

const ARCHIVE_NAME = 'fixture-bundle.tar.gz';

test.describe('Explorer archive listing', () => {
  test.describe.configure({ mode: 'serial' });

  const written: string[] = [];
  test.afterAll(() => {
    // The listing's watcher keeps noticing leftovers, so take the archives out of the repo.
    for (const name of written.splice(0)) {
      const path = join(projectDir(), name);
      if (existsSync(path)) rmSync(path);
    }
  });

  test('a gzipped tar opens as its members, each with a size and a date', async ({ page }) => {
    written.push(ARCHIVE_NAME);
    await gotoProject(page);
    await writeAndOpen(page, ARCHIVE_NAME, gzipSync(tar(MEMBERS)));

    const listing = page.locator('.archive-table');
    await expect(listing).toBeVisible({ timeout: 10000 });
    await expect(listing.locator('thead th')).toHaveText(['Name', 'Size', 'Modified']);

    // Archive order, not a re-sort.
    await expect(listing.locator('tbody .archive-cell-name')).toHaveText(['notes/', 'notes/todo.md', 'report.txt']);

    // A folder row says what it is instead of a size.
    await expect(listing.locator('tbody .archive-cell-size')).toHaveText(['Folder', '30 B', '27 B']);
    // The date column answers "when was this written", so an empty cell is a silent lie.
    await expect(listing.locator('tbody .archive-cell-date').nth(1)).not.toBeEmpty();

    await expect(page.locator('.archive-summary')).toContainText('tar.gz');
    await expect(page.locator('.archive-summary')).toContainText('3 entries');
  });

  test('a member opens as itself, and the listing is still there behind it', async ({ page }) => {
    const name = 'member-read.tar.gz';
    written.push(name);
    await gotoProject(page);
    await writeAndOpen(page, name, gzipSync(tar(MEMBERS)));

    const listing = page.locator('.archive-table');
    await expect(listing).toBeVisible({ timeout: 10000 });
    await listing.locator('tbody .archive-name-btn', { hasText: 'report.txt' }).click();

    await expect(page.locator('.archive-entry-name')).toHaveText('report.txt');
    await expect(page.locator('.archive-entry')).toContainText('one thousand and one lines', { timeout: 10000 });

    // Back is a step within one tab: the listing was fetched once and is not asked for again.
    await page.locator('.archive-back').click();
    await expect(listing).toBeVisible();
    const asks = await page.evaluate(() => performance.getEntriesByType('resource')
      .filter((e) => e.name.includes('/api/fs/archive?')).length);
    await page.locator('.archive-entry-name').waitFor({ state: 'detached' });
    const asksAfter = await page.evaluate(() => performance.getEntriesByType('resource')
      .filter((e) => e.name.includes('/api/fs/archive?')).length);
    expect(asksAfter).toBe(asks);
  });

  test('a folder row is not a way in', async ({ page }) => {
    const name = 'folder-row.tar.gz';
    written.push(name);
    await gotoProject(page);
    await writeAndOpen(page, name, gzipSync(tar(MEMBERS)));

    const listing = page.locator('.archive-table');
    await expect(listing).toBeVisible({ timeout: 10000 });
    expect(await listing.locator('tbody tr').first().locator('button').count()).toBe(0);
  });

  test('a file that only claims an archive suffix says so instead of showing bytes', async ({ page }) => {
    const name = 'not-really.zip';
    written.push(name);
    await gotoProject(page);
    // The suffix chooses the viewer and the bytes decide the answer, so this is the refusal path.
    await writeAndOpen(page, name, Buffer.from('just text with a zip name\n', 'utf8'));

    await expect(page.locator('.archive-note-error')).toContainText('not a readable archive', { timeout: 10000 });
    await expect(page.locator('.archive-table')).toHaveCount(0);
  });
});
