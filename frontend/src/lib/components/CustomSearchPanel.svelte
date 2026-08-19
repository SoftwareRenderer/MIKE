<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import type { EditorView } from 'codemirror';
    import { SelectionRange } from '@codemirror/state';

    interface Props {
        view: EditorView;
        onClose: () => void;
        /**
         * Called on mount with a function that focuses the find input, and on
         * destroy with null. Lets the owner refocus the find input (e.g. when
         * the user pressed Ctrl+F again from the code editor).
         */
        onFocusFind?: (fn: (() => void) | null) => void;
    }

    let { view, onClose, onFocusFind }: Props = $props();

    let inputEl: HTMLInputElement | null = null;
// svelte-ignore non_reactive_update — DOM ref via bind:this
    let replaceEl: HTMLInputElement | null = null;
    let containerEl: HTMLDivElement | null = null;
    let visible = $state(false);

    let matchCase = $state(false);
    let wholeWord = $state(false);
    let regex = $state(false);
    let showReplace = $state(false);
    let matchCount = $state('');
    let currentMatchIndex = $state(0);
    let matches = $state<Array<{ from: number; to: number }>>([]);

    function toggleReplace() {
        showReplace = !showReplace;
        if (showReplace) {
            requestAnimationFrame(() => replaceEl?.focus());
        } else {
            requestAnimationFrame(() => inputEl?.focus());
        }
    }

    function findMatches(): Array<{ from: number; to: number }> {
        const q = inputEl?.value ?? '';
        if (!q) return [];
        const doc = view.state.doc.toString();
        const result: Array<{ from: number; to: number }> = [];

        if (regex) {
            try {
                const flags = matchCase ? 'g' : 'gi';
                const re = new RegExp(q, flags);
                let m;
                while ((m = re.exec(doc)) !== null) {
                    result.push({ from: m.index, to: m.index + m[0].length });
                    if (m[0].length === 0) re.lastIndex++;
                }
            } catch {
                return [];
            }
        } else {
            const searchStr = matchCase ? q : q.toLowerCase();
            const text = matchCase ? doc : doc.toLowerCase();
            let idx = 0;
            while ((idx = text.indexOf(searchStr, idx)) !== -1) {
                if (wholeWord) {
                    const before = doc.charAt(idx - 1) || ' ';
                    const after = doc.charAt(idx + q.length) || ' ';
                    const wordChar = /[a-zA-Z0-9_]/;
                    if (wordChar.test(before) || wordChar.test(after)) {
                        idx += q.length;
                        continue;
                    }
                }
                result.push({ from: idx, to: idx + q.length });
                idx += q.length;
            }
        }
        return result;
    }

    function onInput() {
        matches = findMatches();
        currentMatchIndex = 0;
        matchCount = `${matches.length} match${matches.length !== 1 ? 'es' : ''}`;
        if (matches.length > 0) {
            selectMatch(0);
        }
    }

    function selectMatch(index: number) {
        if (index < 0 || index >= matches.length) return;
        currentMatchIndex = index;
        const m = matches[index];
        view.dispatch({
            selection: (SelectionRange as any).create(m.from, m.to),
        });
        // Scroll into view, offset upward to account for find panel at bottom
        requestAnimationFrame(() => {
            const coords = view.coordsAtPos(m.from);
            if (!coords) return;
            const scroller = view.scrollDOM;
            const panelHeight = 80; // approximate find panel height
            const viewportHeight = scroller.clientHeight;
            const matchBottom = coords.bottom - scroller.scrollTop;
            // If match is in bottom 30% of viewport, scroll up to show it above panel
            if (matchBottom > viewportHeight * 0.3) {
                const targetScroll = scroller.scrollTop + matchBottom - viewportHeight * 0.4;
                scroller.scrollTo({ top: Math.max(0, targetScroll), behavior: 'smooth' });
            } else {
                // Default scroll into view
                scroller.scrollTo({ top: coords.top + scroller.scrollTop - viewportHeight * 0.3, behavior: 'smooth' });
            }
        });
    }

    function selectNext(forward: boolean) {
        if (matches.length === 0) return;
        let next = currentMatchIndex + (forward ? 1 : -1);
        if (next >= matches.length) next = 0;
        if (next < 0) next = matches.length - 1;
        selectMatch(next);
    }

    function doReplace() {
        const r = replaceEl?.value ?? '';
        if (matches.length === 0 || currentMatchIndex < 0) return;
        const m = matches[currentMatchIndex];
        view.dispatch({
            changes: { from: m.from, to: m.to, insert: r },
            selection: { anchor: m.from + r.length },
        });
        // Re-find matches after replacement
        setTimeout(() => onInput(), 0);
    }

    function doReplaceAll() {
        const r = replaceEl?.value ?? '';
        if (matches.length === 0) return;
        // Apply replacements in reverse order to preserve positions
        const changes = matches.slice().reverse().map(m => ({
            from: m.from,
            to: m.to,
            insert: r,
        }));
        view.dispatch({ changes });
        setTimeout(() => onInput(), 0);
    }

    function handleKeydown(e: KeyboardEvent) {
        if (e.key === 'Escape') {
            e.preventDefault();
            e.stopPropagation();
            onClose();
            return;
        }
        if (e.key === 'F3') {
            e.preventDefault();
            e.stopPropagation();
            selectNext(e.shiftKey ? false : true);
            return;
        }
        if (e.key === 'Enter' && !e.shiftKey) {
            if (e.ctrlKey || e.metaKey) {
                e.preventDefault();
                e.stopPropagation();
                doReplaceAll();
                return;
            }
            if (showReplace && document.activeElement === replaceEl) {
                e.preventDefault();
                e.stopPropagation();
                doReplace();
                return;
            }
            e.preventDefault();
            e.stopPropagation();
            selectNext(true);
            return;
        }
        if (e.key === 'Enter' && e.shiftKey) {
            e.preventDefault();
            e.stopPropagation();
            selectNext(false);
            return;
        }
    }

    // Register the find-input focus callback with the owner (FileEditor). Uses
    // $effect (runs right after initial render, cleanup on unmount) rather than
    // onMount so the contract holds in unit tests where onMount is stubbed out.
    $effect(() => {
        onFocusFind?.(() => inputEl?.focus());
        return () => onFocusFind?.(null);
    });

    onMount(() => {
        visible = true;
        inputEl?.focus();

        // On mobile, position panel above the keyboard using visualViewport
        function updatePosition() {
            if (!containerEl) return;
            const vv = typeof visualViewport !== 'undefined' ? visualViewport : null;
            if (vv && window.innerWidth <= 768) {
                const keyboardHeight = Math.max(0, window.innerHeight - vv.height);
                containerEl.style.position = 'fixed';
                containerEl.style.bottom = (keyboardHeight - 1) + 'px';
            } else {
                containerEl.style.position = '';  
                containerEl.style.bottom = '';
            }
        }

        const vv = typeof visualViewport !== 'undefined' ? visualViewport : null;
        vv?.addEventListener('resize', updatePosition);
        window.addEventListener('resize', updatePosition);
        // Initial position after focus triggers keyboard
        setTimeout(updatePosition, 100);

        return () => {
            vv?.removeEventListener('resize', updatePosition);
            window.removeEventListener('resize', updatePosition);
            if (containerEl) {
                containerEl.style.position = '';
                containerEl.style.bottom = '';
            }
        };
    });
</script>

<div class="search-panel-container" bind:this={containerEl}>
    <div class="search-panel" class:visible={visible}>
        <!-- Search row -->
        <div class="search-row">
            <div class="search-input-wrap">
                <input
                    class="search-input"
                    type="text"
                    placeholder="Find"
                    bind:this={inputEl}
                    oninput={onInput}
                    onkeydown={handleKeydown}
                />
                <div class="search-toggles">
                    <button
                        class="toggle-btn"
                        class:active={matchCase}
                        title="Match Case (Alt+C)"
                        aria-pressed={matchCase}
                        onclick={() => { matchCase = !matchCase; onInput(); }}
                    >
                        <svg width="16" height="14" viewBox="0 0 16 14" fill="none" stroke="currentColor" stroke-width="1.2">
                            <text x="1" y="11" font-size="11" font-weight="bold" fill="currentColor" stroke="none" font-family="sans-serif">Aa</text>
                        </svg>
                    </button>
                    <button
                        class="toggle-btn"
                        class:active={wholeWord}
                        title="Whole Word (Alt+W)"
                        aria-pressed={wholeWord}
                        onclick={() => { wholeWord = !wholeWord; onInput(); }}
                    >
                        <svg width="16" height="14" viewBox="0 0 16 14" fill="none" stroke="currentColor" stroke-width="1.2">
                            <text x="1" y="11" font-size="11" font-weight="bold" fill="currentColor" stroke="none" font-family="sans-serif">Ab</text>
                            <line x1="0" y1="12.5" x2="16" y2="12.5"/>
                        </svg>
                    </button>
                    <button
                        class="toggle-btn"
                        class:active={regex}
                        title="Use Regex (Alt+R)"
                        aria-pressed={regex}
                        onclick={() => { regex = !regex; onInput(); }}
                    >
                        <svg width="16" height="14" viewBox="0 0 16 14" fill="none" stroke="currentColor" stroke-width="1.2">
                            <text x="1" y="12" font-size="14" font-weight="bold" fill="currentColor" stroke="none" font-family="sans-serif">.*</text>
                        </svg>
                    </button>
                </div>
                <span class="match-count">{matchCount}</span>
            </div>
            <div class="search-nav">
                <button class="nav-btn" title="Previous match (Shift+F3 / Shift+Enter)" aria-label="Previous match" onclick={() => selectNext(false)}>
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                        <polyline points="18,15 12,9 6,15"/>
                    </svg>
                </button>
                <button class="nav-btn" title="Next match (F3 / Enter)" aria-label="Next match" onclick={() => selectNext(true)}>
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                        <polyline points="6,9 12,15 18,9"/>
                    </svg>
                </button>
                <button class="nav-btn" title="Close (Escape)" aria-label="Close search" onclick={onClose}>
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                        <line x1="18" y1="6" x2="6" y2="18"/>
                        <line x1="6" y1="6" x2="18" y2="18"/>
                    </svg>
                </button>
            </div>
        </div>

        <!-- Replace row (collapsible) -->
        {#if showReplace}
            <div class="replace-row">
                <div class="search-input-wrap replace-wrap">
                    <input
                        class="search-input replace-input"
                        type="text"
                        placeholder="Replace"
                        bind:this={replaceEl}
                        onkeydown={handleKeydown}
                    />
                </div>
                <div class="replace-actions">
                    <button class="action-btn" title="Replace" onclick={doReplace}>Replace</button>
                    <button class="action-btn" title="Replace All (Ctrl+Enter)" onclick={doReplaceAll}>All</button>
                </div>
            </div>
        {:else}
            <button class="toggle-replace-btn" onclick={toggleReplace}>
                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                    <path d="M17 3a2.85 2.83 0 1 1 4 4L7.5 20.5 2 22l1.5-5.5Z"/>
                </svg>
                Replace
            </button>
        {/if}
    </div>
</div>

<style>
    .search-panel-container {
        position: absolute;
        bottom: 0;
        left: 0;
        right: 0;
        z-index: 100;
        pointer-events: none;
    }

    .search-panel {
        width: 100%;
        background: var(--color-surface-1);
        border-top: 1px solid var(--color-border);
        padding: 6px 12px;
        opacity: 0;
        transition: opacity 0.15s ease;
        pointer-events: auto;
    }
    .search-panel.visible {
        opacity: 1;
    }

    .search-row {
        display: flex;
        gap: 6px;
        align-items: center;
    }

    .search-input-wrap {
        flex: 1;
        display: flex;
        align-items: center;
        gap: 0;
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        padding: 0 4px 0 10px;
        min-height: 34px;
    }
    .search-input-wrap:focus-within {
        border-color: var(--color-accent);
        outline: none;
        box-shadow: 0 0 0 1px var(--color-accent)44;
    }

    .search-input {
        flex: 1;
        background: transparent;
        border: none;
        outline: none;
        color: var(--color-text-primary);
        font-family: var(--font-mono, monospace);
        font-size: 13px;
        padding: 6px 0;
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
        padding: 4px 5px;
        border-radius: var(--radius-sm);
        display: flex;
        align-items: center;
        justify-content: center;
        transition: background 0.1s, color 0.1s;
        line-height: 1;
    }
    .toggle-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }
    .toggle-btn.active {
        color: var(--color-accent);
    }

    .match-count {
        font-size: 11px;
        color: var(--color-text-tertiary);
        white-space: nowrap;
        flex-shrink: 0;
        padding: 0 4px;
    }

    .search-nav {
        display: flex;
        gap: 2px;
        flex-shrink: 0;
    }

    .nav-btn {
        background: transparent;
        border: none;
        color: var(--color-text-secondary);
        cursor: pointer;
        padding: 5px;
        border-radius: var(--radius-sm);
        display: flex;
        align-items: center;
        justify-content: center;
        transition: background 0.1s, color 0.1s;
    }
    .nav-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }

    .replace-row {
        display: flex;
        gap: 6px;
        align-items: center;
        margin-top: 6px;
    }

    .replace-wrap {
        flex: 1;
    }

    .replace-input {
        flex: 1;
    }

    .replace-actions {
        display: flex;
        gap: 4px;
        flex-shrink: 0;
    }

    .action-btn {
        background: var(--color-surface-3);
        border: none;
        color: var(--color-text-secondary);
        border-radius: var(--radius-sm);
        padding: 5px 12px;
        cursor: pointer;
        font-size: 12px;
        font-weight: 500;
        font-family: inherit;
        white-space: nowrap;
    }
    .action-btn:hover {
        background: var(--color-surface-4);
        color: var(--color-text-primary);
    }

    .toggle-replace-btn {
        background: transparent;
        border: none;
        color: var(--color-text-tertiary);
        cursor: pointer;
        padding: 4px 8px;
        border-radius: var(--radius-sm);
        display: flex;
        align-items: center;
        gap: 4px;
        font-size: 12px;
        font-family: inherit;
        margin-top: 4px;
        width: 100%;
        justify-content: center;
    }
    .toggle-replace-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-secondary);
    }

    @media (max-width: 768px) {
        .search-panel {
            padding: 4px 4px;
        }
        .search-row {
            gap: 0;
        }
        .search-input-wrap {
            min-height: 44px;
            padding: 0 2px 0 8px;
            flex: 1 1 0;
            min-width: 0;
        }
        .search-input {
            font-size: 16px;
            padding: 8px 0;
        }
        .toggle-btn {
            padding: 8px 6px;
            min-width: 44px;
            flex-shrink: 0;
        }
        .nav-btn {
            padding: 8px;
            min-width: 44px;
            flex-shrink: 0;
        }
        .search-nav {
            gap: 0;
        }
        .match-count {
            display: none;
        }
        .replace-row {
            gap: 4px;
        }
        .replace-wrap {
            min-height: 44px;
        }
        .action-btn {
            min-width: 44px;
            min-height: 44px;
            padding: 8px 12px;
        }
    }
</style>
