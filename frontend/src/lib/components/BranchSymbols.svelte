<script lang="ts">
    let { symbols, onSymbolClick }: {
        symbols: Array<{ name: string; kind: string; line: number; indent: number }>;
        onSymbolClick: (line: number) => void;
    } = $props();

    // Icon mapping for symbol kinds
    function getSymbolIcon(kind: string): string {
        switch (kind) {
            case 'function': return 'ƒ';
            case 'class': return 'C';
            case 'constant': return 'K';
            case 'property': return 'P';
            case 'variable': return 'V';
            case 'heading': return 'H';
            default: return '•';
        }
    }

    function getSymbolColor(kind: string): string {
        switch (kind) {
            case 'function': return '#D2A8FF';
            case 'class': return '#FFA657';
            case 'constant': return '#79C0FF';
            case 'property': return '#79C0FF';
            case 'variable': return '#FFA657';
            case 'heading': return '#7EE787';
            default: return '#AEB2B8';
        }
    }

    /** Check if all symbols are headings (markdown outline mode) */
    let isOutlineMode = $derived(symbols.length > 0 && symbols.every(s => s.kind === 'heading'));

    type HeadingNode = {
        symbol: typeof symbols[0];
        children: HeadingNode[];
        number: string;
    };

    /** Build a nested tree from flat heading list, computing outline numbers */
    function buildHeadingTree(headings: typeof symbols): HeadingNode[] {
        // Track counters per depth level: [h1Count, h2Count, h3Count]
        const counters: number[] = [0, 0, 0];
        const root: { children: HeadingNode[] } = { children: [] };
        const stack: { node: typeof root | HeadingNode; level: number }[] = [{ node: root, level: -1 }];

        for (const sym of headings) {
            const level = sym.indent; // 0, 1, 2
            counters[level]++;

            // Build the outline number: "1", "1.2", "1.2.3"
            const number = counters.slice(0, level + 1).join('.');

            const node: HeadingNode = { symbol: sym, children: [], number };

            // Pop stack until we find a parent at a lower level
            while (stack.length > 1 && stack[stack.length - 1]!.level >= level) {
                stack.pop();
            }

            const parent = stack[stack.length - 1]!;
            parent.node.children.push(node);
            stack.push({ node, level });
        }

        return root.children;
    }

    let headingTree = $derived(isOutlineMode ? buildHeadingTree(symbols) : []);
</script>

<div class="symbol-outline">
    <div class="symbol-header">
        <span class="symbol-label">{isOutlineMode ? 'Outline' : 'Symbols'}</span>
        {#if symbols.length > 0}
            <span class="symbol-count">{symbols.length}</span>
        {/if}
    </div>

    <div class="symbol-list" role="tree" aria-label={isOutlineMode ? 'Document outline' : 'Symbol outline'}>
        {#if symbols.length === 0}
            <div class="symbol-empty">No {isOutlineMode ? 'headings' : 'symbols'}</div>
        {:else if isOutlineMode}
            <!-- Nested tree for markdown headings -->
            {#snippet renderTree(nodes: HeadingNode[], depth: number)}
                <ul class={`outline-tree depth-${depth}`}>
                    {#each nodes as node}
                        <li class="outline-item">
                            <button
                                class="symbol-item"
                                onclick={() => onSymbolClick(node.symbol.line)}
                                role="treeitem"
                                aria-selected="false"
                                title={`Line ${node.symbol.line}: ${node.symbol.name}`}
                            >
                                <span class="outline-number">{node.number}</span>
                                <span class="symbol-name">
                                    {node.symbol.name}
                                </span>
                                <span class="symbol-line">{node.symbol.line}</span>
                            </button>
                            {#if node.children.length > 0}
                                {@render renderTree(node.children, depth + 1)}
                            {/if}
                        </li>
                    {/each}
                </ul>
            {/snippet}
            {@render renderTree(headingTree, 0)}
        {:else}
            <!-- Flat list for code symbols -->
            {#each symbols as symbol}
                <button
                    class="symbol-item"
                    onclick={() => onSymbolClick(symbol.line)}
                    role="treeitem"
                    aria-selected="false"
                    title={`Line ${symbol.line}: ${symbol.name}`}
                >
                    <span
                        class="symbol-icon"
                        style="color: {getSymbolColor(symbol.kind)}"
                    >
                        {getSymbolIcon(symbol.kind)}
                    </span>
                    <span class="symbol-name" style="padding-left: {symbol.indent * 8}px">
                        {symbol.name}
                    </span>
                    <span class="symbol-line">{symbol.line}</span>
                </button>
            {/each}
        {/if}
    </div>
</div>

<style>
    .symbol-outline {
        display: flex;
        flex-direction: column;
        height: 100%;
        overflow: hidden;
    }

    .symbol-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 6px 10px;
        border-bottom: 1px solid var(--color-border);
        flex-shrink: 0;
    }

    .symbol-label {
        font-size: 0.75rem;
        font-weight: 600;
        color: var(--color-text-secondary);
        text-transform: uppercase;
        letter-spacing: 0.05em;
    }

    .symbol-count {
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
        background: var(--color-surface-3);
        padding: 1px 5px;
        border-radius: 8px;
    }

    .symbol-list {
        flex: 1;
        overflow-y: auto;
        overflow-x: hidden;
        padding: 2px 0;
    }

    .symbol-item {
        display: flex;
        align-items: center;
        gap: 4px;
        width: 100%;
        padding: 2px 8px;
        border: none;
        background: transparent;
        color: var(--color-text-primary);
        font-size: 0.75rem;
        text-align: left;
        cursor: pointer;
    }
    .symbol-item:hover {
        background: var(--color-surface-2);
        opacity: 1;
    }

    .symbol-icon {
        flex-shrink: 0;
        width: 14px;
        text-align: center;
        font-size: 0.75rem;
        font-weight: 700;
        font-family: var(--font-mono);
        line-height: 1;
    }

    .symbol-name {
        flex: 1;
        min-width: 0;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
        font-family: var(--font-mono);
    }

    .symbol-line {
        flex-shrink: 0;
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
        opacity: 0.5;
        font-family: var(--font-mono);
    }

    .symbol-empty {
        padding: 8px 10px;
        color: var(--color-text-tertiary);
        font-size: 0.75rem;
    }

    /* Outline tree styles */
    .outline-tree {
        list-style: none;
        margin: 0;
        padding: 0;
    }

    .outline-item {
        list-style: none;
        margin: 0;
        padding: 0;
    }

    .outline-number {
        flex-shrink: 0;
        font-size: 0.6875rem;
        color: var(--color-text-tertiary);
        font-family: var(--font-mono);
        min-width: 14px;
        text-align: left;
        user-select: none;
        margin-right: 6px;
    }

    /* Tighter left padding for outline items vs regular symbols */
    .outline-item > .symbol-item {
        padding-left: 4px;
    }

    /* Depth-based indentation for nested trees */
    .outline-tree.depth-1 {
        margin-left: 8px;
    }
    .outline-tree.depth-2 {
        margin-left: 8px;
    }
</style>
