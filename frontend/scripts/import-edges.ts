// This bundler (vite 8 / rolldown) names the dynamic list `dynamicallyImportedIds`; the older spelling is read
// too, and both are unioned rather than preferred, so a rename answers "no edges" to nobody. A gate that
// misses the dynamic edge says nothing: an ineffective `import()` passes (scripts/split-graph.ts), or the app's
// own lazy chunks land in the precache deny list (scripts/mermaid-chunks.ts).

export function staticEdgeIds(info: unknown): string[] {
    const m = info as { importedIds?: string[] };
    return m.importedIds ?? [];
}

export function dynamicEdgeIds(info: unknown): string[] {
    const m = info as { dynamicImportedIds?: string[]; dynamicallyImportedIds?: string[] };
    return [...new Set([...(m.dynamicallyImportedIds ?? []), ...(m.dynamicImportedIds ?? [])])];
}
