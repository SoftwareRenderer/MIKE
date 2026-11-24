import type { Plugin } from 'vite';
import { dynamicEdgeIds, staticEdgeIds } from './import-edges.ts';

const REPORTED_OFFENDERS = 8;
const REPORTED_IMPORTERS = 4;

/** Paths as the source tree writes them, so a failure points at a file the reader can open. */
function shorten(id: string): string {
    const root = `${process.cwd()}/`;
    return id.startsWith(root) ? id.slice(root.length) : id;
}

function importersOf(holders: ReadonlySet<string>): string {
    const names = [...holders].sort().map(shorten);
    const overflow = names.length - REPORTED_IMPORTERS;
    return overflow > 0 ? `${names.slice(0, REPORTED_IMPORTERS).join(', ')}, +${overflow} more` : names.join(', ');
}

/**
 * Fails the build when a module is dynamically imported from the chunk that already holds it: those bytes are
 * arriving anyway, so the `import()` waits for nothing and the call site claims a laziness the build does not
 * give. The verdict is chunk-relative — `ArchiveViewer.svelte` is imported statically too, by a modal behind
 * the lazily-loaded diff viewer, and its `import()` did earn it a chunk of its own.
 *
 * The bundler only warns, and no gate builds the frontend, so this errors. The idiom costs more than bytes:
 * knip counts a module reached by `import()` as used, so one dead edge over a barrel hides its unused
 * exports. `boot-graph.ts` answers the opposite question — what the first load may evaluate.
 */
export function splitGraphPlugin(): Plugin {
    const statics = new Map<string, Set<string>>();
    const dynamics = new Map<string, Set<string>>();

    function record(into: Map<string, Set<string>>, importer: string, targets: readonly string[]): void {
        for (const target of targets) {
            const holders = into.get(target);
            if (holders) holders.add(importer);
            else into.set(target, new Set([importer]));
        }
    }

    return {
        name: 'mike-split-graph',
        apply: 'build',
        moduleParsed(info) {
            if (!info.id) return;
            record(statics, info.id, staticEdgeIds(info));
            record(dynamics, info.id, dynamicEdgeIds(info));
        },
        generateBundle(_options, bundle) {
            // Either half missing means the walk saw nothing, which must not read as clean.
            if (statics.size + dynamics.size === 0) {
                this.error('split-graph: no import edge was recorded, so nothing here was checked');
            }
            const chunkOf = new Map<string, string>();
            for (const [file, item] of Object.entries(bundle)) {
                if (item.type !== 'chunk') continue;
                for (const id of item.moduleIds) chunkOf.set(id, file);
            }
            if (chunkOf.size === 0) {
                this.error('split-graph: no chunk was built, so nothing here was checked');
            }

            const offenders: string[] = [];
            for (const [target, importers] of [...dynamics].sort(([a], [b]) => a.localeCompare(b))) {
                const alsoStatic = statics.get(target);
                if (!alsoStatic) continue;
                const own = chunkOf.get(target);
                if (own === undefined || ![...importers].some((importer) => chunkOf.get(importer) === own)) continue;
                offenders.push(
                    `  ${shorten(target)} (in ${shorten(own)})\n` +
                    `    dynamically imported by ${importersOf(importers)}\n` +
                    `    statically imported by ${importersOf(alsoStatic)}`,
                );
            }
            if (offenders.length === 0) return;
            const named = offenders.slice(0, REPORTED_OFFENDERS).join('\n');
            const rest = offenders.length - REPORTED_OFFENDERS;
            this.error(
                'split-graph: a module already in its importer\'s chunk cannot move into one of its own, so ' +
                `these dynamic imports wait for bytes that are already loaded:\n${named}` +
                (rest > 0 ? `\n  and ${rest} more` : '') +
                '\nImport it statically, or make the split real: keep every importer an import().',
            );
        },
    };
}
