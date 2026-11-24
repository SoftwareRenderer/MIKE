/**
 * The split-graph guard against a graph and a bundle of its own making. What matters here is that it refuses:
 * a walk that recorded no edges, or read no chunks, would pass every real build — and a real build cannot show
 * its own failure.
 */
import { describe, it, expect } from 'vitest';
import type { Plugin } from 'vite';
import { splitGraphPlugin } from '../../../scripts/split-graph';

type FakeModule = {
    id: string;
    importedIds?: string[];
    dynamicallyImportedIds?: string[];
    dynamicImportedIds?: string[];
};

/** As the bundler names a path, so the guard's shortened refusal matches. */
const abs = (rel: string) => `${process.cwd()}/${rel}`;

const MAIN = abs('src/main.ts');
const BARREL = abs('src/lib/api/index.ts');
const CARD = abs('src/lib/components/Card.svelte');
const EDITOR_TABS = abs('src/lib/components/EditorTabs.svelte');
const DIFF_VIEWER = abs('src/lib/components/DiffViewer.svelte');
const ARCHIVE_MODAL = abs('src/lib/components/ArchiveModal.svelte');
const ARCHIVE_VIEWER = abs('src/lib/components/ArchiveViewer.svelte');

/** Runs the guard over these modules and this chunking, reporting what it refused, or `''` when it passed. */
function refuse(modules: FakeModule[], chunks: Record<string, string[]>): string {
    const plugin = splitGraphPlugin() as Plugin;
    const moduleParsed = plugin.moduleParsed as unknown as (this: unknown, info: unknown) => void;
    const generateBundle = plugin.generateBundle as unknown as (this: unknown, options: unknown, b: unknown) => void;
    const ctx = { error: (message: string): never => { throw new Error(message); } };
    const bundle = Object.fromEntries(
        Object.entries(chunks).map(([name, moduleIds]) => [`assets/${name}.js`, { type: 'chunk', name, moduleIds }]),
    );
    try {
        for (const m of modules) moduleParsed.call(ctx, m);
        generateBundle.call(ctx, {}, bundle);
    } catch (e) {
        return e instanceof Error ? e.message : String(e);
    }
    return '';
}

/** The board's load: entry, api barrel and a card, all in the entry chunk. */
const ENTRY_CHUNK = { index: [MAIN, BARREL, CARD] };

describe('split-graph guard', () => {
    it('refuses a dynamic import from the chunk that already holds the module', () => {
        const refused = refuse(
            [
                { id: MAIN, importedIds: [BARREL] },
                { id: CARD, dynamicallyImportedIds: [BARREL] },
            ],
            ENTRY_CHUNK,
        );
        expect(refused).toContain('src/lib/api/index.ts');
        expect(refused).toContain('dynamically imported by src/lib/components/Card.svelte');
        expect(refused).toContain('statically imported by src/main.ts');
    });

    it('refuses the older spelling of the dynamic edge, so a rename cannot hide one', () => {
        const refused = refuse(
            [
                { id: MAIN, importedIds: [BARREL] },
                { id: CARD, dynamicImportedIds: [BARREL] },
            ],
            ENTRY_CHUNK,
        );
        expect(refused).toContain('src/lib/api/index.ts');
    });

    it('lets a dynamic import whose module has its own chunk stay in the build', () => {
        expect(refuse(
            [
                { id: DIFF_VIEWER, importedIds: [ARCHIVE_MODAL] },
                { id: ARCHIVE_MODAL, importedIds: [ARCHIVE_VIEWER] },
                { id: EDITOR_TABS, dynamicallyImportedIds: [ARCHIVE_VIEWER] },
            ],
            {
                index: [EDITOR_TABS],
                diff: [DIFF_VIEWER, ARCHIVE_MODAL],
                ArchiveViewer: [ARCHIVE_VIEWER],
            },
        )).toBe('');
    });

    it('refuses a build whose edges or chunks it could not read, so a missing hook is not clean', () => {
        expect(refuse([], ENTRY_CHUNK)).toContain('no import edge');
        expect(refuse([{ id: MAIN, importedIds: [BARREL] }], {})).toContain('no chunk was built');
    });
});
