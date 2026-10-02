import type { Plugin } from 'vite';
import { DIAGRAM_NEIGHBOUR_MODULES, MERMAID_MODULES, MERMAID_VENDOR_CHUNK } from './mermaid-chunks.ts';

// The gate walks the entry chunk's static imports and refuses what is named below; a dynamic import
// is not an edge in this walk, it is the fix. Chunk names and module paths are read instead of file
// names, because the build calls any chunk `mmd-*` once no app module reaches it — the bundler's own
// runtime included.

/** Chunks that must sit behind a click, by the name the build gives them. */
export const BOOT_GRAPH_DENY_NAMES: readonly RegExp[] = [
    /^codemirror-core$/,
    /^codemirror-langs$/,
    /^xterm$/,
    /^FileEditor$/,
    /^DiffViewer$/,
    /^CommitDiffOverlay$/,
    /^TerminalPanel$/,
];

/** Modules that must not be evaluated to draw a board, matched against the module's own path. */
export const BOOT_GRAPH_DENY_MODULES: readonly RegExp[] = [MERMAID_MODULES, DIAGRAM_NEIGHBOUR_MODULES];

/**
 * Fails the build when the first load statically reaches a denied chunk or module, or when a pattern
 * names nothing in this build, so a renamed chunk cannot silence the guard.
 */
export function bootGraphPlugin(
    denyNames: readonly RegExp[] = BOOT_GRAPH_DENY_NAMES,
    denyModules: readonly RegExp[] = BOOT_GRAPH_DENY_MODULES,
): Plugin {
    type Node = { name: string; imports: readonly string[]; isEntry: boolean; modules: readonly string[] };
    return {
        name: 'mike-boot-graph',
        apply: 'build',
        generateBundle(_options, bundle) {
            const chunks = new Map<string, Node>();
            for (const [file, item] of Object.entries(bundle)) {
                if (item.type === 'chunk') {
                    chunks.set(file, {
                        name: item.name,
                        imports: item.imports,
                        isEntry: item.isEntry,
                        modules: item.moduleIds,
                    });
                }
            }

            const entry = [...chunks.entries()].find(([, c]) => c.isEntry);
            if (!entry) this.error('boot-graph: no entry chunk, so nothing below was checked');
            const all = [...chunks.values()];

            for (const pattern of denyNames) {
                if (!all.some((c) => pattern.test(c.name))) {
                    this.error(`boot-graph: chunk pattern ${pattern} names no chunk in this build`);
                }
            }
            for (const pattern of denyModules) {
                if (!all.some((c) => c.modules.some((m) => pattern.test(m)))) {
                    this.error(`boot-graph: module pattern ${pattern} names no module in this build`);
                }
            }

            const seen = new Set<string>([entry[0]]);
            const queue = [...entry[1].imports];
            while (queue.length > 0) {
                const file = queue.shift()!;
                if (seen.has(file)) continue;
                seen.add(file);
                const chunk = chunks.get(file);
                if (!chunk) continue; // an asset rather than a chunk: nothing to evaluate
                const deniedName = denyNames.find((pattern) => pattern.test(chunk.name));
                if (deniedName) {
                    this.error(
                        `boot-graph: the first load statically imports "${chunk.name}" (${file}). ` +
                        `Import it dynamically, or move the caller's real dependency somewhere lighter — ` +
                        `see frontend/scripts/boot-graph.ts.`,
                    );
                }
                const denied = chunk.modules.find((m) => denyModules.some((pattern) => pattern.test(m)));
                if (denied) {
                    this.error(
                        `boot-graph: the first load evaluates ${denied}, grouped into "${chunk.name}" ` +
                        `(${file}). Give that vendor block its own chunk — "${MERMAID_VENDOR_CHUNK}" in ` +
                        `frontend/scripts/mermaid-chunks.ts is one.`,
                    );
                }
                queue.push(...chunk.imports);
            }
        },
    };
}
