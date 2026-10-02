import type { Plugin } from 'vite';

/**
 * Which built chunks belong to the diagram renderer rather than to the app shell: a chunk is the
 * renderer's when no module of the app reaches it without passing through mermaid. The service worker
 * precaches every built file, so this decides whether the renderer lands in every install.
 */

/** The renderer's own modules, including the ones it imports per diagram type. */
export const MERMAID_MODULES = /\/node_modules\/mermaid\//;

/** The libraries that reach the application only through the renderer. */
export const DIAGRAM_NEIGHBOUR_MODULES = /\/node_modules\/(d3|dayjs)/;

/** Everything vendored under mermaid itself. */
export const MERMAID_VENDORED_MODULES = /\/node_modules\/mermaid\/node_modules\//;

/** The chunk `vite.config.ts` groups those modules into. */
export const MERMAID_VENDOR_CHUNK = 'mermaid-vendor';

/** Names the renderer's chunks, which is how the deny list below can refuse them. */
export const MERMAID_CHUNK_PREFIX = 'mmd-';

/** Out of the precache; these are fetched on demand. */
export const MERMAID_PRECACHE_DENY = `**/${MERMAID_CHUNK_PREFIX}*.js`;

/** Module ids the app reaches without walking through mermaid. Until `buildEnd` fills the set the answer
 * is "not the renderer's", because an empty set would make every chunk look like the renderer's. */
let appReached = new Set<string>();
let computed = false;

/** Both spellings of the dynamic-import edge. This bundler (vite 8 / rolldown) offers only
 * `dynamicallyImportedIds`, and a missed edge would move the app's own lazy chunks into the deny list. */
function edgeIds(info: unknown): string[] {
    const m = info as { importedIds?: string[]; dynamicImportedIds?: string[]; dynamicallyImportedIds?: string[] };
    return [...(m.importedIds ?? []), ...(m.dynamicImportedIds ?? m.dynamicallyImportedIds ?? [])];
}

/** Collects import edges and the entry set, then answers reachability once the build has ended. */
export function mermaidChunksPlugin(): Plugin {
    const edges = new Map<string, string[]>();
    const entries = new Set<string>();
    let mermaidInGraph = false;
    return {
        name: 'mike-mermaid-chunks',
        apply: 'build',
        moduleParsed(info) {
            if (!info.id) return;
            if (MERMAID_MODULES.test(info.id)) mermaidInGraph = true;
            edges.set(info.id, edgeIds(info));
            if (info.isEntry) entries.add(info.id);
        },
        buildEnd() {
            const reached = new Set<string>();
            const queue = [...entries];
            while (queue.length) {
                const id = queue.pop()!;
                if (reached.has(id) || MERMAID_MODULES.test(id)) continue;
                reached.add(id);
                for (const next of edges.get(id) ?? []) queue.push(next);
            }
            appReached = reached;
            computed = true;
        },
        generateBundle(_options, bundle) {
            // A bundler that stopped calling the file-name hook would re-add the megabytes silently.
            if (!mermaidInGraph) return;
            const named = Object.keys(bundle).some((file) => file.includes(`/${MERMAID_CHUNK_PREFIX}`));
            if (!named) this.error('no chunk was named for the diagram renderer: the precache deny list is matching nothing');
        },
    };
}

/** A chunk holding a module the app also reaches is shell code, not the renderer's. */
export function isMermaidChunk(moduleIds: readonly string[]): boolean {
    if (!computed || moduleIds.length === 0) return false;
    return moduleIds.every((id) => !appReached.has(id));
}
