<script lang="ts">
    import VirtualList from '$lib/components/VirtualList.svelte';
    import { loadAppSetting, saveAppSetting } from '$lib/storage';

    interface Sym {
        name: string;
        kind: string;
        line: number;
        endLine?: number;
        indent: number;
        containerName?: string;
        children?: Sym[];
    }

    let { symbols, onSymbolClick }: {
        symbols: Sym[];
        onSymbolClick: (line: number) => void;
    } = $props();

    // Tree view toggle (persisted)
    let treeView = $state(false);
    loadAppSetting('symbols-tree-view', false).then(v => { treeView = !!v; });
    function toggleTreeView() {
        treeView = !treeView;
        saveAppSetting('symbols-tree-view', treeView);
    }

    // Alphabetical sort toggle (persisted, default: true)
    let sortAlpha = $state(true);
    loadAppSetting('symbols-sort-alpha', true).then(v => { sortAlpha = v !== false; });
    function toggleSortAlpha() {
        sortAlpha = !sortAlpha;
        saveAppSetting('symbols-sort-alpha', sortAlpha);
    }

    // Collapsed nodes tracking
    let collapsedIds = $state(new Set<string>());
    function toggleCollapse(id: string) {
        if (collapsedIds.has(id)) {
            collapsedIds.delete(id);
        } else {
            collapsedIds.add(id);
        }
    }

    // Icon mapping for symbol kinds
    function getSymbolIcon(kind: string): string {
        switch (kind) {
            case 'function': return 'ƒ';
            case 'method': return 'ƒ';
            case 'class': return 'C';
            case 'struct': return 'S';
            case 'enum': return 'E';
            case 'interface': return 'I';
            case 'constant': return 'K';
            case 'property': return 'P';
            case 'field': return 'F';
            case 'variable': return 'V';
            case 'module': return 'M';
            case 'namespace': return 'N';
            case 'package': return 'P';
            case 'heading': return 'H';
            default: return '•';
        }
    }

    function getSymbolColor(kind: string): string {
        switch (kind) {
            case 'function': return '#D2A8FF';
            case 'method': return '#D2A8FF';
            case 'class': return '#FFA657';
            case 'struct': return '#FFA657';
            case 'enum': return '#FFA657';
            case 'interface': return '#FFA657';
            case 'constant': return '#79C0FF';
            case 'property': return '#79C0FF';
            case 'field': return '#79C0FF';
            case 'variable': return '#FFA657';
            case 'module': return '#7EE787';
            case 'namespace': return '#7EE787';
            case 'package': return '#7EE787';
            case 'heading': return '#7EE787';
            default: return '#AEB2B8';
        }
    }

    /** Check if all symbols are headings (markdown outline mode) */
    let isOutlineMode = $derived(symbols.length > 0 && symbols.every(s => s.kind === 'heading'));

    /** Top-level declarations only for flat view (no nested/local symbols) */
    let flatList = $derived.by(() => {
        let list = symbols.filter(s => !s.containerName);
        if (sortAlpha) {
            list = [...list].sort((a, b) => a.name.localeCompare(b.name));
        }
        return list;
    });

    /** Generate a unique ID for a symbol node */
    function symbolId(sym: typeof symbols[0], index: number, parentPath: string = ''): string {
        return parentPath ? `${parentPath}/${index}` : `${index}`;
    }

    /** Flatten tree symbols into a list with depth info, respecting collapsed state */
    type TreeItem = { symbol: typeof symbols[0]; depth: number; id: string; hasChildren: boolean };
    let flatTree = $derived.by(() => {
        if (!treeView || isOutlineMode) return [];
        const flat: TreeItem[] = [];
        function walk(nodes: typeof symbols, depth: number, parentPath: string) {
            let sorted = nodes;
            if (sortAlpha) {
                sorted = [...nodes].sort((a, b) => a.name.localeCompare(b.name));
            }
            for (let i = 0; i < sorted.length; i++) {
                const sym = sorted[i];
                const id = symbolId(sym, i, parentPath);
                const hasChildren = !!(sym.children && sym.children.length > 0);
                flat.push({ symbol: sym, depth, id, hasChildren });
                if (hasChildren && !collapsedIds.has(id)) {
                    walk(sym.children!, depth + 1, id);
                }
            }
        }
        walk(symbols, 0, '');
        return flat;
    });

    // Outline mode: headings re-nested by level (h1 > h2 > h3) and numbered, which is
    // what the outline renders — `flatHeadings` below is its virtual-list source.
    type HeadingNode = {
        symbol: typeof symbols[0];
        children: HeadingNode[];
        number: string;
    };

    function buildHeadingTree(headings: typeof symbols): HeadingNode[] {
        const counters: number[] = [0, 0, 0];
        const root: { children: HeadingNode[] } = { children: [] };
        const stack: { node: typeof root | HeadingNode; level: number }[] = [{ node: root, level: -1 }];

        for (const sym of headings) {
            const level = sym.indent;
            counters[level]++;
            const number = counters.slice(0, level + 1).join('.');
            const node: HeadingNode = { symbol: sym, children: [], number };
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

    let flatHeadings = $derived.by(() => {
        if (!isOutlineMode) return [];
        const flat: Array<{ node: HeadingNode; depth: number }> = [];
        function walk(nodes: HeadingNode[], depth: number) {
            for (const node of nodes) {
                flat.push({ node, depth });
                if (node.children.length > 0) walk(node.children, depth + 1);
            }
        }
        walk(headingTree, 0);
        return flat;
    });
</script>

<div class="symbol-outline">
    <div class="symbol-header">
        <span class="symbol-label">{isOutlineMode ? 'Outline' : 'Symbols'}</span>
        <div class="symbol-header-actions">
            {#if symbols.length > 0 && !isOutlineMode}
                <button
                    class={`toggle-btn ${sortAlpha ? 'active' : ''}`}
                    onclick={toggleSortAlpha}
                    title={sortAlpha ? 'Sort by position' : 'Sort alphabetically'}
                    aria-label={sortAlpha ? 'Switch to position sort' : 'Switch to alphabetical sort'}
                >
                    {#if sortAlpha}
                        <!-- 1-2-3 icon (click to switch to position sort) -->
                        <svg width="14" height="14" viewBox="0 0 16 16" fill="currentColor">
                            <text x="2" y="6" font-size="6" font-weight="700" font-family="sans-serif">1</text>
                            <text x="2" y="11" font-size="6" font-weight="700" font-family="sans-serif">2</text>
                            <text x="2" y="16" font-size="6" font-weight="700" font-family="sans-serif">3</text>
                            <text x="9" y="6" font-size="6" font-weight="700" font-family="sans-serif">4</text>
                            <text x="9" y="11" font-size="6" font-weight="700" font-family="sans-serif">5</text>
                            <text x="9" y="16" font-size="6" font-weight="700" font-family="sans-serif">6</text>
                        </svg>
                    {:else}
                        <!-- A→Z icon (click to switch to alphabetical sort) -->
                        <svg width="14" height="14" viewBox="0 0 16 16" fill="currentColor">
                            <text x="1" y="12" font-size="10" font-weight="700" font-family="sans-serif">A</text>
                            <text x="9" y="12" font-size="10" font-weight="700" font-family="sans-serif">Z</text>
                        </svg>
                    {/if}
                </button>
            {/if}
            {#if symbols.length > 0 && !isOutlineMode && symbols.some(s => s.children && s.children.length > 0)}
                <button
                    class="toggle-btn"
                    onclick={toggleTreeView}
                    title={treeView ? 'Flat list' : 'Tree view'}
                    aria-label={treeView ? 'Switch to flat list' : 'Switch to tree view'}
                >
                    {#if treeView}
                        <!-- Flat list icon -->
                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5">
                            <line x1="4" y1="3" x2="14" y2="3"/>
                            <line x1="4" y1="7.5" x2="14" y2="7.5"/>
                            <line x1="4" y1="12" x2="10" y2="12"/>
                            <circle cx="2" cy="3" r="0.8" fill="currentColor" stroke="none"/>
                            <circle cx="2" cy="7.5" r="0.8" fill="currentColor" stroke="none"/>
                            <circle cx="2" cy="12" r="0.8" fill="currentColor" stroke="none"/>
                        </svg>
                    {:else}
                        <!-- Tree icon -->
                        <svg width="14" height="14" viewBox="0 0 16 16" fill="currentColor">
                            <circle cx="8" cy="3" r="2"/>
                            <circle cx="3" cy="12" r="2"/>
                            <circle cx="13" cy="12" r="2"/>
                            <line x1="6.8" y1="4.2" x2="4" y2="10.2" stroke="currentColor" stroke-width="1.2"/>
                            <line x1="9.2" y1="4.2" x2="12" y2="10.2" stroke="currentColor" stroke-width="1.2"/>
                        </svg>
                    {/if}
                </button>
            {/if}
            {#if symbols.length > 0}
                <span class="symbol-count">{symbols.length}</span>
            {/if}
        </div>
    </div>

    {#if symbols.length === 0}
        <div class="symbol-list">
            <div class="symbol-empty">No {isOutlineMode ? 'headings' : 'symbols'}</div>
        </div>
    {:else if isOutlineMode}
        <VirtualList items={flatHeadings} itemHeight={22} minThreshold={50} role="tree" aria-label="Document outline">
            {#snippet children({ item: h })}
                <div class={`outline-item depth-${h.depth}`}>
                    <button
                        class="symbol-item"
                        onclick={() => onSymbolClick(h.node.symbol.line)}
                        role="treeitem"
                        aria-selected="false"
                        title={`Line ${h.node.symbol.line}: ${h.node.symbol.name}`}
                    >
                        <span class="outline-number">{h.node.number}</span>
                        <span class="symbol-name">
                            {h.node.symbol.name}
                        </span>
                        <span class="symbol-line">{h.node.symbol.line}</span>
                    </button>
                </div>
            {/snippet}
        </VirtualList>
    {:else if treeView}
        <!-- Tree view for LSP symbols -->
        <VirtualList items={flatTree} itemHeight={22} minThreshold={50} role="tree" aria-label="Symbol tree">
            {#snippet children({ item })}
                <div class={`tree-item depth-${item.depth}`}>
                    <button
                        class="symbol-item"
                        onclick={() => onSymbolClick(item.symbol.line)}
                        role="treeitem"
                        aria-selected="false"
                        {...(item.hasChildren ? { 'aria-expanded': !collapsedIds.has(item.id) } : {})}
                        title={`Line ${item.symbol.line}: ${item.symbol.name}`}
                    >
                        {#if item.hasChildren}
                            <span
                                class="collapse-icon"
                                onclick={(e) => { e.stopPropagation(); toggleCollapse(item.id); }}
                                onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.stopPropagation(); toggleCollapse(item.id); } }}
                                role="button"
                                tabindex="0"
                                aria-label={collapsedIds.has(item.id) ? 'Expand' : 'Collapse'}
                            >
                                {#if collapsedIds.has(item.id)}
                                    <svg width="10" height="10" viewBox="0 0 10 10" fill="currentColor">
                                        <path d="M3 1l4 4-4 4" stroke="currentColor" stroke-width="1.5" fill="none"/>
                                    </svg>
                                {:else}
                                    <svg width="10" height="10" viewBox="0 0 10 10" fill="currentColor">
                                        <path d="M1 3l4 4 4-4" stroke="currentColor" stroke-width="1.5" fill="none"/>
                                    </svg>
                                {/if}
                            </span>
                        {/if}
                        <span
                            class="symbol-icon"
                            style="color: {getSymbolColor(item.symbol.kind)}"
                        >
                            {getSymbolIcon(item.symbol.kind)}
                        </span>
                        <span class="symbol-name">
                            {item.symbol.name}
                        </span>
                        <span class="symbol-line">{item.symbol.line}</span>
                    </button>
                </div>
            {/snippet}
        </VirtualList>
    {:else}
        <!-- Flat list for code symbols (top-level only) -->
        <VirtualList items={flatList} itemHeight={22} minThreshold={50} role="tree" aria-label="Symbol outline">
            {#snippet children({ item: symbol })}
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
            {/snippet}
        </VirtualList>
    {/if}
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

    .symbol-header-actions {
        display: flex;
        align-items: center;
        gap: 6px;
    }

    .toggle-btn {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 20px;
        height: 20px;
        padding: 0;
        border: none;
        background: transparent;
        color: var(--color-text-tertiary);
        cursor: pointer;
        border-radius: 4px;
    }
    .toggle-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-secondary);
    }
    .toggle-btn.active {
        color: var(--color-text-secondary);
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

    /* Collapse/expand icon */
    .collapse-icon {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 14px;
        height: 14px;
        flex-shrink: 0;
        cursor: pointer;
        color: var(--color-text-tertiary);
        border-radius: 2px;
    }
    .collapse-icon:hover {
        color: var(--color-text-secondary);
    }

    /* Outline styles (flattened for virtualization) */
    .outline-item {
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

    .outline-item > .symbol-item {
        padding-left: 4px;
    }

    .outline-item.depth-1 {
        padding-left: 8px;
    }
    .outline-item.depth-2 {
        padding-left: 16px;
    }
    .outline-item.depth-3 {
        padding-left: 24px;
    }

    /* Tree view styles */
    .tree-item {
        margin: 0;
        padding: 0;
    }

    .tree-item.depth-0 {
        padding-left: 4px;
    }
    .tree-item.depth-1 {
        padding-left: 16px;
    }
    .tree-item.depth-2 {
        padding-left: 28px;
    }
    .tree-item.depth-3 {
        padding-left: 40px;
    }
    .tree-item.depth-4 {
        padding-left: 52px;
    }
    .tree-item.depth-5 {
        padding-left: 64px;
    }
</style>
