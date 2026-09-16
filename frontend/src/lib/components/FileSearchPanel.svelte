<script lang="ts">
    import { onDestroy } from 'svelte';
    import { t } from '$lib/i18n';
    import { escapeHtml } from '$lib/utils/format';

    interface SearchResult {
        path: string;
        line?: number;
        text?: string;
    }

    let { projectId, taskId, onFileSelected, onClose }: {
        projectId: string;
        taskId?: string;
        onFileSelected: (path: string, line?: number) => void;
        onClose: () => void;
    } = $props();

    let inputEl: HTMLInputElement | null = null;

    let query = $state('');
    let matchCase = $state(false);
    let wholeWord = $state(false);
    let useRegex = $state(false);
    let searchContent = $state(true);

    let results = $state<SearchResult[]>([]);
    let rootPath = $state('');
    let loading = $state(false);
    let error = $state('');
    let searched = $state(false);

    interface GroupedFile {
        path: string;
        line: number;
        results: SearchResult[];
    }

    let groupedResults = $derived.by(() => {
        const map = new Map<string, SearchResult[]>();
        for (const r of results) {
            const key = r.path;
            const group = map.get(key);
            if (group) {
                group.push(r);
            } else {
                map.set(key, [r]);
            }
        }
        const groups: GroupedFile[] = [];
        const sortedPaths = [...map.keys()].sort((a, b) => a.localeCompare(b));
        for (const path of sortedPaths) {
            const fileResults = map.get(path)!;
            fileResults.sort((a, b) => (a.line || 0) - (b.line || 0));
            groups.push({ path, line: fileResults[0].line || 0, results: fileResults });
        }
        return groups;
    });

    // Debounce timer
    let debounceTimer: ReturnType<typeof setTimeout> | null = null;

    function scheduleSearch() {
        if (debounceTimer) clearTimeout(debounceTimer);
        debounceTimer = setTimeout(doSearch, 300);
    }

    async function doSearch() {
        const q = query.trim();
        if (!q) {
            results = [];
            searched = false;
            error = '';
            return;
        }

        loading = true;
        error = '';
        searched = true;

        try {
            const params = new URLSearchParams({
                q,
                project: projectId,
            });
            if (taskId) params.set('task', taskId);
            if (searchContent) params.set('content', '1');
            if (matchCase) params.set('case', '1');
            if (wholeWord) params.set('word', '1');
            if (useRegex) params.set('regex', '1');

            const res = await fetch(`/api/fs/search?${params}`);
            if (!res.ok) {
                const err = await res.json().catch(() => ({}));
                throw new Error(err.error || t('editor.searchFailed', { status: res.status }));
            }
            const data = await res.json();
            rootPath = data.root || '';
            results = (data.results || []) as SearchResult[];
        } catch (e) {
            error = String(e);
            results = [];
        } finally {
            loading = false;
        }
    }

    function handleInput() {
        scheduleSearch();
    }

    function handleKeydown(e: KeyboardEvent) {
        if (e.key === 'Escape') {
            e.preventDefault();
            e.stopPropagation();
            onClose();
            return;
        }
        if (e.key === 'Enter') {
            // Trigger immediate search on Enter
            e.preventDefault();
            if (debounceTimer) clearTimeout(debounceTimer);
            doSearch();
            return;
        }
    }

    function handleResultClick(result: SearchResult) {
        const fullPath = rootPath ? rootPath + '/' + result.path : result.path;
        onFileSelected(fullPath, result.line || undefined);
    }

    // Highlight matching text in a line. The regex half can hang the tab on text the repository
    // supplies, and only a worker would bound a pattern the user typed; left open on purpose.
    function highlightMatch(line: string, q: string): string {
        if (!q || !line) return escapeHtml(line);
        try {
            if (useRegex) {
                const execRe = new RegExp(q, matchCase ? 'g' : 'gi');
                const parts: string[] = [];
                let lastIdx = 0;
                let m: RegExpExecArray | null;
                while ((m = execRe.exec(line)) !== null) {
                    if (m.index > lastIdx) {
                        parts.push(escapeHtml(line.substring(lastIdx, m.index)));
                    }
                    parts.push(`<mark>${escapeHtml(m[0])}</mark>`);
                    lastIdx = m.index + m[0].length;
                    if (m[0].length === 0) execRe.lastIndex++;
                }
                if (lastIdx < line.length) {
                    parts.push(escapeHtml(line.substring(lastIdx)));
                }
                return parts.length > 0 ? parts.join('') : escapeHtml(line);
            }
            // Plain text search — highlight all occurrences
            const searchStr = matchCase ? q : q.toLowerCase();
            const text = matchCase ? line : line.toLowerCase();
            const parts: string[] = [];
            let lastIdx = 0;
            let idx = 0;
            while ((idx = text.indexOf(searchStr, idx)) !== -1) {
                if (idx > lastIdx) {
                    parts.push(escapeHtml(line.substring(lastIdx, idx)));
                }
                parts.push(`<mark>${escapeHtml(line.substring(idx, idx + q.length))}</mark>`);
                idx += q.length;
                lastIdx = idx;
            }
            if (lastIdx < line.length) {
                parts.push(escapeHtml(line.substring(lastIdx)));
            }
            return parts.length > 0 ? parts.join('') : escapeHtml(line);
        } catch {
            return escapeHtml(line);
        }
    }


    function getMatchCount(): string {
        if (!searched || !query.trim()) return '';
        if (loading) return t('editor.searching');
        if (results.length === 0) return t('editor.noResults');
        return t('editor.results', { count: results.length });
    }

    // Focus input on mount
    $effect(() => {
        requestAnimationFrame(() => inputEl?.focus());
    });

    onDestroy(() => {
        if (debounceTimer) clearTimeout(debounceTimer);
    });
</script>

<div class="file-search-panel">
    <!-- Search bar -->
    <div class="search-bar">
        <div class="search-input-wrap">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="search-icon" aria-hidden="true">
                <circle cx="11" cy="11" r="8"/>
                <line x1="21" y1="21" x2="16.65" y2="16.65"/>
            </svg>
            <input
                class="search-input"
                type="text"
                placeholder={searchContent ? 'Search...' : 'Find filename...'}
                bind:value={query}
                bind:this={inputEl}
                oninput={handleInput}
                onkeydown={handleKeydown}
                aria-label="Search files"
            />
            <div class="search-toggles">
                <button
                    class="toggle-btn"
                    class:active={matchCase}
                    title="Match Case"
                    aria-pressed={matchCase}
                    onclick={() => { matchCase = !matchCase; handleInput(); }}
                >
                    <svg width="14" height="12" viewBox="0 0 14 12" fill="none" aria-hidden="true">
                        <text x="0" y="10" font-size="10" font-weight="bold" fill="currentColor" stroke="none" font-family="sans-serif">Aa</text>
                    </svg>
                </button>
                <button
                    class="toggle-btn"
                    class:active={wholeWord}
                    title="Whole Word"
                    aria-pressed={wholeWord}
                    onclick={() => { wholeWord = !wholeWord; handleInput(); }}
                >
                    <svg width="16" height="12" viewBox="0 0 16 12" fill="none" aria-hidden="true">
                        <text x="0" y="10" font-size="10" font-weight="bold" fill="currentColor" stroke="none" font-family="sans-serif">Ab</text>
                        <line x1="0" y1="11" x2="16" y2="11" stroke="currentColor" stroke-width="1"/>
                    </svg>
                </button>
                <button
                    class="toggle-btn"
                    class:active={useRegex}
                    title="Use Regex"
                    aria-pressed={useRegex}
                    onclick={() => { useRegex = !useRegex; handleInput(); }}
                >
                    <svg width="14" height="12" viewBox="0 0 14 12" fill="none" aria-hidden="true">
                        <text x="0" y="10" font-size="11" font-weight="bold" fill="currentColor" stroke="none" font-family="monospace">.*</text>
                    </svg>
                </button>
                <button
                    class="toggle-btn"
                    class:active={!searchContent}
                    title="Search Filenames Only"
                    aria-pressed={!searchContent}
                    onclick={() => { searchContent = !searchContent; handleInput(); }}
                >
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                        <path d="M3 1h6l4 4v10a1 1 0 01-1 1H3a1 1 0 01-1-1V2a1 1 0 011-1z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round"/>
                        <path d="M9 1v4h4" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round"/>
                        <line x1="5" y1="8" x2="11" y2="8" stroke="currentColor" stroke-width="1" stroke-linecap="round"/>
                        <line x1="5" y1="10.5" x2="9" y2="10.5" stroke="currentColor" stroke-width="1" stroke-linecap="round"/>
                    </svg>
                </button>
            </div>
            {#if query}
                <button class="clear-btn" onclick={() => { query = ''; results = []; searched = false; error = ''; inputEl?.focus(); }} aria-label="Clear search">
                    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true">
                        <line x1="18" y1="6" x2="6" y2="18"/>
                        <line x1="6" y1="6" x2="18" y2="18"/>
                    </svg>
                </button>
            {:else}
                <button class="clear-btn" onclick={onClose} aria-label="Close search" title="Close (Escape)">
                    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true">
                        <line x1="18" y1="6" x2="6" y2="18"/>
                        <line x1="6" y1="6" x2="18" y2="18"/>
                    </svg>
                </button>
            {/if}
        </div>
    </div>

    <!-- Results -->
    {#if error}
        <div class="search-error">{error}</div>
    {:else if loading}
        <div class="search-loading">{t('editor.searching')}</div>
    {:else if searched && results.length === 0}
        <div class="search-empty">{t('editor.noResultsFound')}</div>
    {:else if results.length > 0}
        <div class="search-results" role="list" aria-label="Search results">
            {#each groupedResults as group}
                <!-- File header -->
                <div class="result-file-header" title={group.path}>
                    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" class="file-icon" aria-hidden="true">
                        <path d="M14 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V8z"/>
                        <polyline points="14,2 14,8 20,8"/>
                    </svg>
                    <span class="file-path">{group.path}</span>
                    <span class="file-match-count">{t('editor.matches', { count: group.results.length })}</span>
                </div>
                <!-- Results for this file -->
                {#each group.results as result}
                    {#if result.line}
                        <button
                            class="search-result-item"
                            onclick={() => handleResultClick(result)}
                            title={result.path}
                        >
                            <div class="result-line">
                                <span class="line-num">{result.line}</span>
                                <span class="line-text">{@html highlightMatch(result.text || '', query)}</span>
                            </div>
                        </button>
                    {:else}
                        <button
                            class="search-result-item"
                            onclick={() => handleResultClick(result)}
                            title={result.path}
                        >
                            <div class="result-path">
                                {result.path}
                            </div>
                        </button>
                    {/if}
                {/each}
            {/each}
        </div>
    {:else}
        <div class="search-placeholder">
            <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" class="placeholder-icon" aria-hidden="true">
                <circle cx="11" cy="11" r="8"/>
                <line x1="21" y1="21" x2="16.65" y2="16.65"/>
            </svg>
            <p>{t('editor.searchInProject')}</p>
        </div>
    {/if}

    <!-- Footer bar -->
    <div class="search-footer">
        <span class="match-count">{getMatchCount()}</span>
    </div>
</div>

<style>
    .file-search-panel {
        display: flex;
        flex-direction: column;
        height: 100%;
        overflow: hidden;
    }

    .search-bar {
        padding: 6px 8px;
        flex-shrink: 0;
    }

    .search-input-wrap {
        display: flex;
        align-items: center;
        gap: 0;
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        padding: 0 4px 0 8px;
        min-height: 28px;
    }
    .search-input-wrap:focus-within {
        border-color: var(--color-accent);
        box-shadow: 0 0 0 1px color-mix(in srgb, var(--color-accent) 27%, transparent);
    }

    .search-icon {
        color: var(--color-text-tertiary);
        flex-shrink: 0;
        margin-right: 6px;
    }

    .search-input {
        flex: 1;
        background: transparent;
        border: none;
        outline: none;
        color: var(--color-text-primary);
        font-family: var(--font-mono, monospace);
        font-size: 0.75rem;
        padding: 4px 0;
        min-width: 0;
    }
    .search-input::placeholder {
        color: var(--color-text-tertiary);
    }

    .search-toggles {
        display: flex;
        gap: 0;
        flex-shrink: 0;
    }

    .toggle-btn {
        background: transparent;
        border: none;
        color: var(--color-text-tertiary);
        cursor: pointer;
        padding: 3px 4px;
        border-radius: var(--radius-sm);
        display: flex;
        align-items: center;
        justify-content: center;
        line-height: 1;
    }
    .toggle-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .toggle-btn.active {
        color: var(--color-accent);
    }

    .clear-btn {
        background: none;
        border: none;
        color: var(--color-text-tertiary);
        cursor: pointer;
        padding: 2px;
        display: flex;
        align-items: center;
        justify-content: center;
        border-radius: 2px;
        flex-shrink: 0;
    }
    .clear-btn:hover {
        color: var(--color-text-primary);
    }

    .search-error {
        padding: 12px;
        color: var(--color-status-red);
        font-size: 0.75rem;
    }

    .search-loading {
        padding: 12px;
        color: var(--color-text-secondary);
        font-size: 0.75rem;
        text-align: center;
    }

    .search-empty {
        padding: 12px;
        color: var(--color-text-tertiary);
        font-size: 0.75rem;
        text-align: center;
    }

    .search-placeholder {
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        gap: 8px;
        padding: 24px 12px;
        color: var(--color-text-tertiary);
        font-size: 0.75rem;
        flex: 1;
    }
    .placeholder-icon {
        opacity: 0.3;
    }

    .search-results {
        flex: 1;
        overflow-y: auto;
        overflow-x: hidden;
        padding: 2px 0;
    }

    .result-file-header {
        display: flex;
        align-items: center;
        gap: 6px;
        padding: 4px 10px;
        background: var(--color-surface-2);
        cursor: default;
    }

    .file-path {
        flex: 1;
        min-width: 0;
        font-family: var(--font-mono, monospace);
        font-size: 0.6875rem;
        font-weight: 500;
        color: var(--color-text-primary);
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }

    .file-match-count {
        font-size: 0.6875rem;
        color: var(--color-text-tertiary);
        flex-shrink: 0;
    }

    .search-result-item {
        display: block;
        width: 100%;
        padding: 2px 10px 2px 16px;
        border: none;
        background: transparent;
        color: var(--color-text-primary);
        font-size: 0.75rem;
        text-align: left;
        cursor: pointer;
        border-radius: 0;
    }
    .search-result-item:hover {
        background: var(--color-surface-2);
    }

    .result-path {
        color: var(--color-text-secondary);
        font-family: var(--font-mono, monospace);
        font-size: 0.6875rem;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
        display: flex;
        align-items: center;
        gap: 4px;
    }

    .file-icon {
        flex-shrink: 0;
        opacity: 0.5;
    }

    .result-line {
        display: flex;
        gap: 6px;
        font-family: var(--font-mono, monospace);
        font-size: 0.6875rem;
        line-height: 1.5;
        margin-top: 1px;
        padding-left: 16px;
    }

    .line-num {
        color: var(--color-text-tertiary);
        flex-shrink: 0;
        min-width: 2ch;
        text-align: right;
        user-select: none;
    }

    .line-text {
        white-space: pre;
        overflow: hidden;
        text-overflow: ellipsis;
        flex: 1;
        min-width: 0;
    }
    .line-text :global(mark) {
        background: color-mix(in srgb, var(--color-accent) 35%, transparent);
        color: inherit;
        border-radius: 2px;
        padding: 0 1px;
    }

    .search-footer {
        display: flex;
        align-items: center;
        padding: 4px 10px;
        border-top: 1px solid var(--color-border);
        background: var(--color-surface-1);
        flex-shrink: 0;
    }

    .search-footer .match-count {
        font-size: 0.6875rem;
        color: var(--color-text-tertiary);
        white-space: nowrap;
    }

    @media (max-width: 768px) {
        .search-bar {
            padding: 8px 10px;
        }
        .search-input-wrap {
            min-height: 40px;
            padding: 0 8px;
        }
        .search-input {
            font-size: 16px; /* Prevent zoom on iOS */
            padding: 6px 0;
        }
        .toggle-btn {
            padding: 6px 8px;
            min-width: 36px;
        }
        .search-result-item {
            padding: 8px 12px;
        }
        .result-path {
            font-size: 0.8125rem;
        }
        .result-line {
            font-size: 0.8125rem;
            padding-left: 20px;
        }
    }
</style>
